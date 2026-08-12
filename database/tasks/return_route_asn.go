package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	returnRouteASNLookupTimeout   = 6 * time.Second
	returnRouteASNProviderTimeout = 1500 * time.Millisecond
)

type asnCacheEntry struct {
	asn     int
	expires time.Time
}

type returnRouteASNProvider func(context.Context, net.IP) (int, error)

var (
	asnCache = struct {
		sync.RWMutex
		values map[string]asnCacheEntry
	}{values: map[string]asnCacheEntry{}}
	returnRouteASNHTTPClient = &http.Client{Timeout: 2 * time.Second}
)

func lookupASNsWithRules(ips []string, rules *compiledReturnRouteRules) map[string]int {
	unique := map[string]struct{}{}
	for _, ip := range ips {
		unique[ip] = struct{}{}
	}
	result := make(map[string]int, len(unique))
	ctx, cancel := context.WithTimeout(context.Background(), returnRouteASNLookupTimeout)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for ip := range unique {
		ip := ip
		wg.Add(1)
		go func() {
			defer wg.Done()
			asn := lookupASNWithRules(ctx, ip, rules)
			mu.Lock()
			result[ip] = asn
			mu.Unlock()
		}()
	}
	wg.Wait()
	return result
}

func lookupASNWithRules(ctx context.Context, value string, rules *compiledReturnRouteRules) int {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return 0
	}
	if asn := localReturnRouteASN(ip.String(), rules); asn > 0 {
		return asn
	}
	key := ip.String()
	asnCache.RLock()
	cached, ok := asnCache.values[key]
	asnCache.RUnlock()
	if ok && cached.expires.After(time.Now()) {
		return cached.asn
	}
	asn := lookupASNWithProviders(ctx, ip, []returnRouteASNProvider{
		lookupASNCymru,
		lookupASNRIPEStat,
		lookupASNBGPView,
	})
	cacheTTL := 5 * time.Minute
	if asn > 0 {
		cacheTTL = 24 * time.Hour
	}
	asnCache.Lock()
	asnCache.values[key] = asnCacheEntry{asn: asn, expires: time.Now().Add(cacheTTL)}
	asnCache.Unlock()
	return asn
}

func lookupASNWithProviders(ctx context.Context, ip net.IP, providers []returnRouteASNProvider) int {
	for _, provider := range providers {
		providerCtx, cancel := context.WithTimeout(ctx, returnRouteASNProviderTimeout)
		asn, err := provider(providerCtx, ip)
		cancel()
		if err == nil && asn > 0 {
			return asn
		}
		if ctx.Err() != nil {
			return 0
		}
	}
	return 0
}

func lookupASNCymru(ctx context.Context, ip net.IP) (int, error) {
	texts, err := net.DefaultResolver.LookupTXT(ctx, cymruQueryName(ip))
	if err != nil {
		return 0, err
	}
	for _, text := range texts {
		fields := strings.Fields(strings.ReplaceAll(text, "|", " "))
		if len(fields) == 0 {
			continue
		}
		asn, err := strconv.Atoi(strings.TrimPrefix(strings.ToUpper(fields[0]), "AS"))
		if err == nil && asn > 0 {
			return asn, nil
		}
	}
	return 0, fmt.Errorf("Cymru 未返回有效 ASN")
}

func lookupASNRIPEStat(ctx context.Context, ip net.IP) (int, error) {
	endpoint := "https://stat.ripe.net/data/network-info/data.json?resource=" + url.QueryEscape(ip.String())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("User-Agent", "Komari-Return-Route")
	response, err := returnRouteASNHTTPClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("RIPEstat 返回 HTTP %d", response.StatusCode)
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			ASNs []json.RawMessage `json:"asns"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return 0, err
	}
	for _, raw := range payload.Data.ASNs {
		if asn := parseASNValue(raw); asn > 0 {
			return asn, nil
		}
	}
	return 0, fmt.Errorf("RIPEstat 未返回有效 ASN")
}

func lookupASNBGPView(ctx context.Context, ip net.IP) (int, error) {
	endpoint := "https://api.bgpview.io/ip/" + url.PathEscape(ip.String())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("User-Agent", "Komari-Return-Route")
	response, err := returnRouteASNHTTPClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("BGPView 返回 HTTP %d", response.StatusCode)
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Prefixes []struct {
				Prefix    string          `json:"prefix"`
				ASN       json.RawMessage `json:"asn"`
				OriginASN json.RawMessage `json:"origin_asn"`
			} `json:"prefixes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return 0, err
	}
	bestASN, bestMask := 0, -1
	for _, prefix := range payload.Data.Prefixes {
		_, network, err := net.ParseCIDR(prefix.Prefix)
		if err != nil || !network.Contains(ip) {
			continue
		}
		mask, _ := network.Mask.Size()
		asn := parseASNValue(prefix.ASN)
		if asn == 0 {
			asn = parseASNValue(prefix.OriginASN)
		}
		if asn > 0 && mask > bestMask {
			bestASN, bestMask = asn, mask
		}
	}
	if bestASN == 0 {
		return 0, fmt.Errorf("BGPView 未返回有效 ASN")
	}
	return bestASN, nil
}

func parseASNValue(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		return number
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		number, _ = strconv.Atoi(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(text)), "AS"))
		return number
	}
	var object struct {
		ASN    int `json:"asn"`
		Number int `json:"number"`
	}
	if err := json.Unmarshal(raw, &object); err == nil {
		if object.ASN > 0 {
			return object.ASN
		}
		return object.Number
	}
	return 0
}

func cymruQueryName(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", v4[3], v4[2], v4[1], v4[0])
	}
	hex := fmt.Sprintf("%032x", ip.To16())
	chars := strings.Split(hex, "")
	for left, right := 0, len(chars)-1; left < right; left, right = left+1, right-1 {
		chars[left], chars[right] = chars[right], chars[left]
	}
	return strings.Join(chars, ".") + ".origin6.asn.cymru.com"
}
