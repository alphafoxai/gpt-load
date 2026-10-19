package models

// AffinityBinding 保存会话到上游账号的软亲和绑定。BindingKey 是 HMAC 摘要，
// 不含提示词、访问密钥或凭据内容。IdentityGeneration 按位保存 uint64。
type AffinityBinding struct {
	BindingKey         string `gorm:"column:binding_key;type:varchar(64);primaryKey;not null"`
	GroupID            uint   `gorm:"column:group_id;not null"`
	CredentialID       uint   `gorm:"column:credential_id;not null"`
	IdentityGeneration int64  `gorm:"column:identity_generation;not null"`
	ExpiresAtMS        int64  `gorm:"column:expires_at_ms;not null"`
	UpdatedAtMS        int64  `gorm:"column:updated_at_ms;not null"`
}

func (AffinityBinding) TableName() string { return "affinity_bindings" }
