package messageSender

import "fmt"

type EmptyProvider struct{}

func (e *EmptyProvider) SendTextMessage(message, title string) error {
	return fmt.Errorf("you are using an empty message sender, please check your configuration")
}
