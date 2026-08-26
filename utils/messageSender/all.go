package messageSender

import (
	_ "github.com/nuomiiiii/lite/utils/messageSender/bark"
	_ "github.com/nuomiiiii/lite/utils/messageSender/email"
	_ "github.com/nuomiiiii/lite/utils/messageSender/empty"
	_ "github.com/nuomiiiii/lite/utils/messageSender/javascript"
	_ "github.com/nuomiiiii/lite/utils/messageSender/serverchan3"
	_ "github.com/nuomiiiii/lite/utils/messageSender/serverchanturbo"
	_ "github.com/nuomiiiii/lite/utils/messageSender/telegram"
	_ "github.com/nuomiiiii/lite/utils/messageSender/webhook"
)

func All() {
}
