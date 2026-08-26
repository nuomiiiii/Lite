package models

import "time"

// BillingPriceVersion is an immutable snapshot of a client's billing terms.
type BillingPriceVersion struct {
	ID               uint64     `json:"id" gorm:"primaryKey;autoIncrement"`
	Client           string     `json:"client" gorm:"type:varchar(36);not null;index"`
	ClientName       string     `json:"client_name" gorm:"type:varchar(100);not null;default:''"`
	Region           string     `json:"region" gorm:"type:varchar(100);not null;default:''"`
	Group            string     `json:"group" gorm:"type:varchar(100);not null;default:''"`
	PriceMicros      int64      `json:"price_micros" gorm:"type:bigint;not null"`
	Currency         string     `json:"currency" gorm:"type:varchar(20);not null"`
	CurrencyValid    bool       `json:"currency_valid" gorm:"not null;default:false"`
	FXSnapshotID     *uint64    `json:"fx_snapshot_id,omitempty" gorm:"index"`
	USDPriceMicros   *int64     `json:"usd_price_micros,omitempty" gorm:"type:bigint"`
	BillingCycleDays int        `json:"billing_cycle_days" gorm:"type:int;not null"`
	ExpiredAt        *time.Time `json:"expired_at,omitempty" gorm:"type:timestamp"`
	EffectiveFrom    time.Time  `json:"effective_from" gorm:"type:timestamp;not null;index"`
	EffectiveTo      *time.Time `json:"effective_to,omitempty" gorm:"type:timestamp;index"`
	Source           string     `json:"source" gorm:"type:varchar(24);not null"`
	CreatedAt        time.Time  `json:"created_at"`
}

// BillingFXSnapshot stores one complete, immutable reference-rate response.
type BillingFXSnapshot struct {
	ID           uint64    `json:"id" gorm:"primaryKey;autoIncrement"`
	Provider     string    `json:"provider" gorm:"type:varchar(32);not null"`
	BaseCurrency string    `json:"base_currency" gorm:"type:varchar(3);not null;default:'USD'"`
	RatesJSON    string    `json:"rates_json" gorm:"type:text;not null"`
	FetchedAt    time.Time `json:"fetched_at" gorm:"type:timestamp;not null;index"`
	CreatedAt    time.Time `json:"created_at"`
}

// BillingEntry is an append-only expense ledger row. Reversals are new rows.
type BillingEntry struct {
	ID                   uint64    `json:"id" gorm:"primaryKey;autoIncrement"`
	EntryKey             string    `json:"entry_key" gorm:"type:varchar(160);not null;uniqueIndex"`
	Client               string    `json:"client" gorm:"type:varchar(36);not null;index"`
	ClientName           string    `json:"client_name" gorm:"type:varchar(100);not null;default:''"`
	Type                 string    `json:"type" gorm:"type:varchar(24);not null;index"`
	Day                  string    `json:"day" gorm:"type:varchar(10);not null;index"`
	OccurredAt           time.Time `json:"occurred_at" gorm:"type:timestamp;not null;index"`
	OriginalAmountMicros int64     `json:"original_amount_micros" gorm:"type:bigint;not null"`
	OriginalCurrency     string    `json:"original_currency" gorm:"type:varchar(20);not null;index"`
	FXSnapshotID         *uint64   `json:"fx_snapshot_id,omitempty" gorm:"index"`
	USDAmountMicros      *int64    `json:"usd_amount_micros,omitempty" gorm:"type:bigint"`
	PriceVersionID       *uint64   `json:"price_version_id,omitempty" gorm:"index"`
	ReversalOf           *uint64   `json:"reversal_of,omitempty" gorm:"index"`
	SourceRef            string    `json:"source_ref" gorm:"type:varchar(128);not null;default:'';index"`
	Note                 string    `json:"note" gorm:"type:varchar(500);not null;default:''"`
	Operator             string    `json:"operator" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt            time.Time `json:"created_at"`
}
