package cmd

import (
	logger "github.com/nuomiiiii/lite/utils/log"
)

func reportTransparentHugePages() {
	if err := disableTransparentHugePages(); err != nil {
		logger.Warnf("server", "disable transparent huge pages: %v", err)
		return
	}
	if !transparentHugePagesApplied() {
		return
	}
	logger.Infof("server", "transparent huge pages disabled")
}
