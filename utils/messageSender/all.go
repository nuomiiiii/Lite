package messageSender

import (
	_ "github.com/komari-monitor/komari/utils/messageSender/email"
	_ "github.com/komari-monitor/komari/utils/messageSender/empty"
	_ "github.com/komari-monitor/komari/utils/messageSender/telegram"
)

func All() {
}
