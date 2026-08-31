package tasks

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

const (
	mainlandLineStateStable    = "stable"
	mainlandLineStateSwitching = "switching"
	mainlandLineStateRebasing  = "rebasing"

	mainlandMinTruncationTimeouts = 3
	mainlandAnchorWindow          = 3
	mainlandAnchorHits            = 2
	mainlandRecentPathLimit       = 5
)

type mainlandPathHop struct {
	TTL     int
	IP      string
	ASN     int
	Prefix  string
	Timeout bool
	Public  bool
}

type mainlandProbeClassification struct {
	Outcome         string
	ClassifiedLine  string
	LineState       string
	Signature       string
	TerminalTTL     int
	TerminalAnchor  string
	TargetReached   bool
	BaselineVersion int
	TruncationNote  string
	Comparable      bool
}

func inferReturnRouteTargetReached(task models.ReturnRouteTask, result v2.RouteResultParams) (string, bool) {
	resolved := strings.TrimSpace(result.ResolvedTargetIP)
	if resolved == "" {
		if ip := net.ParseIP(strings.Trim(strings.TrimSpace(task.Target), "[]")); ip != nil {
			if (task.IPVersion == 4 && ip.To4() != nil) || (task.IPVersion == 6 && ip.To4() == nil) {
				resolved = ip.String()
			}
		}
	}
	if result.TargetReached {
		return resolved, true
	}
	dest := net.ParseIP(resolved)
	if dest == nil {
		return resolved, false
	}
	for _, hop := range result.Hops {
		if hop.Timeout {
			continue
		}
		if ip := net.ParseIP(strings.TrimSpace(hop.IP)); ip != nil && ip.Equal(dest) {
			return resolved, true
		}
	}
	return resolved, false
}

func buildMainlandPathHops(hops []v2.RouteHop, asns map[string]int) []mainlandPathHop {
	path := make([]mainlandPathHop, 0, len(hops))
	for _, hop := range hops {
		item := mainlandPathHop{TTL: hop.TTL, Timeout: hop.Timeout || strings.TrimSpace(hop.IP) == ""}
		if item.Timeout {
			path = append(path, item)
			continue
		}
		item.IP = strings.TrimSpace(hop.IP)
		item.ASN = asns[item.IP]
		item.Prefix = mainlandIPPrefix(item.IP)
		item.Public = isPublicReturnRouteIP(item.IP)
		path = append(path, item)
	}
	return path
}

func mainlandIPPrefix(value string) string {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.0/24", v4[0], v4[1], v4[2])
	}
	parts := strings.Split(ip.String(), ":")
	if len(parts) >= 3 {
		return strings.Join(parts[:3], ":") + "::/48"
	}
	return ip.String() + "/48"
}

func mainlandHopKey(hop mainlandPathHop) string {
	if hop.Timeout || hop.IP == "" {
		return fmt.Sprintf("%d *", hop.TTL)
	}
	if hop.ASN > 0 {
		return fmt.Sprintf("%d AS%d %s", hop.TTL, hop.ASN, hop.Prefix)
	}
	if hop.Prefix != "" {
		return fmt.Sprintf("%d %s", hop.TTL, hop.Prefix)
	}
	return fmt.Sprintf("%d %s", hop.TTL, hop.IP)
}

func mainlandAnchorKey(hop mainlandPathHop) string {
	if hop.Timeout || hop.IP == "" {
		return ""
	}
	if hop.ASN > 0 && hop.Prefix != "" {
		return fmt.Sprintf("AS%d %s", hop.ASN, hop.Prefix)
	}
	if hop.Prefix != "" {
		return hop.Prefix
	}
	return hop.IP
}

func mainlandPathSignature(path []mainlandPathHop) string {
	keys := make([]string, 0, len(path))
	for _, hop := range path {
		if hop.Timeout {
			continue
		}
		if hop.Public || hop.ASN > 0 {
			keys = append(keys, mainlandHopKey(hop))
		}
	}
	return strings.Join(keys, "|")
}

func mainlandTerminalHop(path []mainlandPathHop, targetReached bool, resolved string) (int, string) {
	if targetReached && strings.TrimSpace(resolved) != "" {
		return lastRespondingTTL(path), "target " + resolved
	}
	for i := len(path) - 1; i >= 0; i-- {
		hop := path[i]
		if hop.Timeout || hop.IP == "" {
			continue
		}
		if hop.Public || hop.ASN > 0 {
			return hop.TTL, mainlandAnchorKey(hop)
		}
	}
	return 0, ""
}

func lastRespondingTTL(path []mainlandPathHop) int {
	for i := len(path) - 1; i >= 0; i-- {
		if !path[i].Timeout && path[i].IP != "" {
			return path[i].TTL
		}
	}
	return 0
}

func publicHopCount(path []mainlandPathHop) int {
	count := 0
	for _, hop := range path {
		if hop.Public {
			count++
		}
	}
	return count
}

func mainlandLineState(status models.ReturnRouteStatus, classified, expected string) string {
	classified = strings.TrimSpace(classified)
	if classified == "UNKNOWN" {
		classified = ""
	}
	if status.State == "observing" && strings.TrimSpace(status.CandidateLine) != "" && !isPendingReturnRouteLine(status.CandidateLine) {
		return mainlandLineStateSwitching
	}
	current := strings.TrimSpace(status.CurrentLine)
	ref := current
	if ref == "" {
		ref = normalizeReturnRouteLine(expected)
	}
	if classified != "" && ref != "" && classified != ref && !isPendingReturnRouteLine(classified) {
		if status.State == "observing" {
			return mainlandLineStateSwitching
		}
		if status.State == "switched" && (!status.BaselineReady || status.BaselineLine != status.CurrentLine) {
			return mainlandLineStateRebasing
		}
	}
	if status.State == "switched" && (!status.BaselineReady || status.BaselineLine != strings.TrimSpace(status.CurrentLine)) {
		return mainlandLineStateRebasing
	}
	return mainlandLineStateStable
}

func classifyMainlandReachability(
	agentError string,
	path []mainlandPathHop,
	classified string,
	expected string,
	status models.ReturnRouteStatus,
	targetReached bool,
	resolved string,
) mainlandProbeClassification {
	classified = strings.TrimSpace(classified)
	if classified == "UNKNOWN" {
		classified = ""
	}
	result := mainlandProbeClassification{
		ClassifiedLine:  classified,
		LineState:       mainlandLineState(status, classified, expected),
		Signature:       mainlandPathSignature(path),
		TargetReached:   targetReached,
		BaselineVersion: status.BaselineVersion,
	}
	result.TerminalTTL, result.TerminalAnchor = mainlandTerminalHop(path, targetReached, resolved)
	if strings.TrimSpace(agentError) != "" {
		result.Outcome = mainlandOutcomeInvalid
		return result
	}
	if result.LineState == mainlandLineStateSwitching {
		result.Outcome = mainlandOutcomeIndeterminate
		return result
	}
	if targetReached {
		result.Outcome = mainlandOutcomeReachable
		result.Comparable = true
		if result.LineState == mainlandLineStateRebasing {
			result.Outcome = mainlandOutcomeIndeterminate
			result.Comparable = true
		}
		return result
	}
	baselineReady := status.BaselineReady && status.BaselineLine != "" && status.BaselineLine == strings.TrimSpace(status.CurrentLine)
	if result.LineState == mainlandLineStateRebasing || !baselineReady {
		result.Outcome = mainlandOutcomeIndeterminate
		result.Comparable = publicHopCount(path) > 0 && result.TerminalAnchor != ""
		return result
	}
	if publicHopCount(path) == 0 {
		result.Outcome = mainlandOutcomeTruncated
		result.TruncationNote = "全部跳点超时，未达到历史终点锚点"
		return result
	}
	outcome, note := compareMainlandPathToBaseline(path, status)
	result.Outcome = outcome
	result.TruncationNote = note
	result.Comparable = outcome == mainlandOutcomeReachable
	return result
}

func compareMainlandPathToBaseline(path []mainlandPathHop, status models.ReturnRouteStatus) (string, string) {
	baselineHops := parseMainlandSignatureHops(status.BaselineRouteSignature)
	terminal := strings.TrimSpace(status.BaselineTerminalAnchor)
	reachedEquivalent := false
	lastMatchedTTL := 0
	maxCurrentTTL := 0
	for _, hop := range path {
		if hop.Timeout || hop.IP == "" {
			continue
		}
		if hop.TTL > maxCurrentTTL {
			maxCurrentTTL = hop.TTL
		}
		anchor := mainlandAnchorKey(hop)
		if terminal != "" && anchorsMatch(anchor, terminal, hop) {
			if hop.TTL >= status.BaselineTerminalTTL-1 {
				reachedEquivalent = true
			}
		}
		if baselineHasAnchor(baselineHops, hop) && hop.TTL > lastMatchedTTL {
			lastMatchedTTL = hop.TTL
		}
	}
	if status.BaselineTerminalTTL > 0 && maxCurrentTTL >= status.BaselineTerminalTTL {
		reachedEquivalent = true
	}
	if reachedEquivalent {
		return mainlandOutcomeReachable, ""
	}
	lastResponseTTL := lastRespondingTTL(path)
	trailing := trailingTimeouts(path, lastResponseTTL)
	matchedEarlier := lastMatchedTTL > 0 && (status.BaselineTerminalTTL == 0 || lastMatchedTTL < status.BaselineTerminalTTL)
	if matchedEarlier && trailing >= mainlandMinTruncationTimeouts && baselineHasLaterResponse(baselineHops, lastResponseTTL) {
		note := "提前截断"
		if lastMatched := baselineAnchorAt(baselineHops, lastMatchedTTL); lastMatched != "" {
			note = fmt.Sprintf("在 %s 后提前截断", lastMatched)
		} else if terminal != "" {
			note = fmt.Sprintf("未达到历史终点锚点 %s", terminal)
		}
		return mainlandOutcomeTruncated, note
	}
	return mainlandOutcomeIndeterminate, ""
}

func baselineAnchorAt(baseline []mainlandPathHop, ttl int) string {
	for _, hop := range baseline {
		if hop.TTL == ttl {
			return mainlandAnchorKey(hop)
		}
	}
	return ""
}

func trailingTimeouts(path []mainlandPathHop, afterTTL int) int {
	count := 0
	for _, hop := range path {
		if hop.TTL <= afterTTL {
			continue
		}
		if hop.Timeout || hop.IP == "" {
			count++
			continue
		}
		return 0
	}
	return count
}

func anchorsMatch(anchor, terminal string, hop mainlandPathHop) bool {
	if anchor == "" || terminal == "" {
		return false
	}
	if anchor == terminal {
		return true
	}
	if hop.Prefix != "" && strings.Contains(terminal, hop.Prefix) {
		return true
	}
	if hop.ASN > 0 && strings.Contains(terminal, fmt.Sprintf("AS%d", hop.ASN)) {
		return true
	}
	return false
}

func parseMainlandSignatureHops(signature string) []mainlandPathHop {
	if strings.TrimSpace(signature) == "" {
		return nil
	}
	parts := strings.Split(signature, "|")
	hops := make([]mainlandPathHop, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var ttl, asn int
		var prefix string
		if _, err := fmt.Sscanf(part, "%d AS%d %s", &ttl, &asn, &prefix); err == nil {
			hops = append(hops, mainlandPathHop{TTL: ttl, ASN: asn, Prefix: prefix, Public: true})
			continue
		}
		if _, err := fmt.Sscanf(part, "%d %s", &ttl, &prefix); err == nil && prefix != "*" {
			hops = append(hops, mainlandPathHop{TTL: ttl, Prefix: prefix, Public: true})
		}
	}
	return hops
}

func baselineHasAnchor(baseline []mainlandPathHop, hop mainlandPathHop) bool {
	anchor := mainlandAnchorKey(hop)
	for _, item := range baseline {
		if mainlandAnchorKey(item) == anchor || (item.Prefix != "" && item.Prefix == hop.Prefix) {
			return true
		}
	}
	return false
}

func baselineHasLaterResponse(baseline []mainlandPathHop, cutoffTTL int) bool {
	for _, hop := range baseline {
		if hop.TTL > cutoffTTL {
			return true
		}
	}
	return false
}

func updateMainlandBaseline(status *models.ReturnRouteStatus, class mainlandProbeClassification, stableLine string, now time.Time) {
	if status == nil || class.LineState == mainlandLineStateSwitching {
		return
	}
	line := strings.TrimSpace(stableLine)
	if line == "" || isPendingReturnRouteLine(line) {
		return
	}
	if class.Outcome == mainlandOutcomeTruncated || class.Outcome == mainlandOutcomeInvalid {
		return
	}
	if !class.TargetReached && class.TerminalAnchor == "" {
		return
	}
	if status.BaselineReady && status.BaselineLine == line && class.Outcome != mainlandOutcomeReachable && !class.TargetReached {
		return
	}
	if status.BaselineLine != "" && status.BaselineLine != line {
		status.BaselineReady = false
		status.BaselineRecent = ""
		status.BaselineVersion++
	}
	status.BaselineLine = line
	recent := appendMainlandRecentAnchor(status.BaselineRecent, class.TerminalAnchor)
	status.BaselineRecent = recent
	if class.TargetReached {
		markMainlandBaselineReady(status, class, true, now)
		return
	}
	if electMainlandAnchor(recent) == class.TerminalAnchor || countRecentAnchor(recent, class.TerminalAnchor) >= mainlandAnchorHits {
		markMainlandBaselineReady(status, class, false, now)
	}
}

func markMainlandBaselineReady(status *models.ReturnRouteStatus, class mainlandProbeClassification, targetReached bool, now time.Time) {
	status.BaselineReady = true
	status.BaselineTargetReached = targetReached
	status.BaselineTerminalTTL = class.TerminalTTL
	status.BaselineTerminalAnchor = class.TerminalAnchor
	status.BaselineRouteSignature = class.Signature
	status.BaselineUpdatedAt = &now
	if status.BaselineVersion == 0 {
		status.BaselineVersion = 1
	}
}

func appendMainlandRecentAnchor(raw, anchor string) string {
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		return raw
	}
	var items []string
	_ = json.Unmarshal([]byte(raw), &items)
	items = append(items, anchor)
	if len(items) > mainlandRecentPathLimit {
		items = items[len(items)-mainlandRecentPathLimit:]
	}
	encoded, _ := json.Marshal(items)
	return string(encoded)
}

func countRecentAnchor(raw, anchor string) int {
	var items []string
	if json.Unmarshal([]byte(raw), &items) != nil {
		return 0
	}
	window := items
	if len(window) > mainlandAnchorWindow {
		window = window[len(window)-mainlandAnchorWindow:]
	}
	count := 0
	for _, item := range window {
		if item == anchor {
			count++
		}
	}
	return count
}

func electMainlandAnchor(raw string) string {
	var items []string
	if json.Unmarshal([]byte(raw), &items) != nil {
		return ""
	}
	window := items
	if len(window) > mainlandAnchorWindow {
		window = window[len(window)-mainlandAnchorWindow:]
	}
	counts := map[string]int{}
	for _, item := range window {
		counts[item]++
	}
	best := ""
	bestCount := 0
	for anchor, count := range counts {
		if count > bestCount {
			best = anchor
			bestCount = count
		}
	}
	if bestCount >= mainlandAnchorHits {
		return best
	}
	return ""
}
