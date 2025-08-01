package factory

type IMessageSender interface {
	GetName() string
	// 请务必返回 &Configuration{} 的指针
	GetConfiguration() Configuration
	SendTextMessage(message, title string) error
	Init() error
	Destroy() error
}

type Configuration interface{}

type MessageSenderConstructor func() IMessageSender
