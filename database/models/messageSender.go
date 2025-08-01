package models

type MessageSenderProvider struct {
	Name     string `json:"name" gorm:"primaryKey;unique;not null"`
	Addition string `json:"addition" gorm:"type:longtext" default:"{}"`
}
