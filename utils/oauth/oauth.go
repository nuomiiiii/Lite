package oauth

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/komari-monitor/komari/utils/oauth/factory"
)

var (
	currentProvider factory.IOidcProvider
	mu              = sync.Mutex{}
)

func CurrentProvider() factory.IOidcProvider {
	mu.Lock()
	defer mu.Unlock()
	return currentProvider
}

func LoadProvider(name string, configJson string) error {
	mu.Lock()
	defer mu.Unlock()
	if currentProvider != nil && currentProvider.GetName() == name {
		return nil // 已经加载了相同的提供程序
	}
	if currentProvider != nil {
		if err := currentProvider.Destroy(); err != nil {
			log.Printf("Failed to destroy provider %s: %v", currentProvider.GetName(), err)
		}
	}
	constructor, exists := factory.GetConstructor(name)
	if !exists {
		return fmt.Errorf("provider %s not found", name)
	}
	currentProvider = constructor()
	if err := json.Unmarshal([]byte(configJson), currentProvider.GetConfiguration()); err != nil {
		return fmt.Errorf("failed to unmarshal config for provider %s: %w", name, err)
	}
	err := currentProvider.Init()
	if err != nil {
		return fmt.Errorf("failed to initialize provider %s: %w", name, err)
	}
	return nil
}
