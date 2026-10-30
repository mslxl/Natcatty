package store

// The store keeps aggregate JSON alongside indexed projections. These models
// describe the physical schema used by both AutoMigrate and repository ORM
// operations.

type metaSchema struct {
	Key   string `gorm:"column:key;type:text;primaryKey"`
	Value string `gorm:"column:value;type:text;not null"`
}

func (metaSchema) TableName() string { return "meta" }

type hostSchema struct {
	ID        string `gorm:"column:id;type:text;primaryKey"`
	Label     string `gorm:"column:label;type:text;not null"`
	Hostname  string `gorm:"column:hostname;type:text;not null"`
	Port      int64  `gorm:"column:port;not null;default:0"`
	Username  string `gorm:"column:username;type:text;not null;default:''"`
	Protocol  string `gorm:"column:protocol;type:text;not null;default:'ssh'"`
	GroupPath string `gorm:"column:group_path;type:text;not null;default:'';index:hosts_group_order,priority:1"`
	Pinned    int64  `gorm:"column:pinned;type:integer;not null;default:0;index:hosts_recent,priority:1,sort:desc"`
	OrderKey  int64  `gorm:"column:order_key;not null;default:0;index:hosts_group_order,priority:2"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null;index:hosts_recent,priority:2,sort:desc"`
	Data      string `gorm:"column:data;type:text;not null"`
}

func (hostSchema) TableName() string { return "hosts" }

type cryptoKeySchema struct {
	Version         int64  `gorm:"column:version;primaryKey"`
	Purpose         string `gorm:"column:purpose;type:text;not null"`
	CreatedAt       int64  `gorm:"column:created_at;not null"`
	RetiredAt       *int64 `gorm:"column:retired_at"`
	WrappedKey      []byte `gorm:"column:wrapped_key;type:blob"`
	WrapNonce       []byte `gorm:"column:wrap_nonce;type:blob"`
	WrapKDF         string `gorm:"column:wrap_kdf;type:text"`
	WrapSalt        []byte `gorm:"column:wrap_salt;type:blob"`
	WrapTime        *int64 `gorm:"column:wrap_time"`
	WrapMemory      *int64 `gorm:"column:wrap_memory"`
	WrapParallelism *int64 `gorm:"column:wrap_parallelism"`
}

func (cryptoKeySchema) TableName() string { return "crypto_keys" }

type secretValueSchema struct {
	EntityType string `gorm:"column:entity_type;type:text;not null;primaryKey"`
	EntityID   string `gorm:"column:entity_id;type:text;not null;primaryKey"`
	Field      string `gorm:"column:field;type:text;not null;primaryKey"`
	Alg        string `gorm:"column:alg;type:text;not null"`
	KeyVersion int64  `gorm:"column:key_version;not null;index:secret_values_key_version"`
	Nonce      []byte `gorm:"column:nonce;type:blob;not null"`
	Ciphertext []byte `gorm:"column:ciphertext;type:blob;not null"`
	UpdatedAt  int64  `gorm:"column:updated_at;not null"`
}

func (secretValueSchema) TableName() string { return "secret_values" }

type knownHostSchema struct {
	ID                string `gorm:"column:id;type:text;primaryKey"`
	Hostname          string `gorm:"column:hostname;type:text;not null;collate:nocase;uniqueIndex:known_hosts_selector,priority:1;index:known_hosts_endpoint,priority:1"`
	Port              int64  `gorm:"column:port;not null;default:22;uniqueIndex:known_hosts_selector,priority:2;index:known_hosts_endpoint,priority:2"`
	KeyType           string `gorm:"column:key_type;type:text;not null;uniqueIndex:known_hosts_selector,priority:3"`
	Fingerprint       string `gorm:"column:fingerprint;type:text;not null;default:''"`
	PublicKey         string `gorm:"column:public_key;type:text;not null;default:''"`
	DiscoveredAt      int64  `gorm:"column:discovered_at;not null"`
	LastSeen          int64  `gorm:"column:last_seen;not null"`
	ConvertedToHostID string `gorm:"column:converted_to_host_id;type:text;not null;default:''"`
	OrderKey          int64  `gorm:"column:order_key;not null;default:0"`
	Data              string `gorm:"column:data;type:text;not null"`
}

func (knownHostSchema) TableName() string { return "known_hosts" }

type sshKeySchema struct {
	ID        string `gorm:"column:id;type:text;primaryKey"`
	Label     string `gorm:"column:label;type:text;not null;index:ssh_keys_order,priority:2,collate:nocase"`
	Type      string `gorm:"column:type;type:text;not null;default:''"`
	Source    string `gorm:"column:source;type:text;not null;default:'imported'"`
	OrderKey  int64  `gorm:"column:order_key;not null;default:0;index:ssh_keys_order,priority:1"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
	Data      string `gorm:"column:data;type:text;not null"`
}

func (sshKeySchema) TableName() string { return "ssh_keys" }

type identitySchema struct {
	ID         string `gorm:"column:id;type:text;primaryKey"`
	Label      string `gorm:"column:label;type:text;not null;index:identities_order,priority:2,collate:nocase"`
	Username   string `gorm:"column:username;type:text;not null;default:''"`
	AuthMethod string `gorm:"column:auth_method;type:text;not null;default:'password'"`
	KeyID      string `gorm:"column:key_id;type:text;not null;default:''"`
	OrderKey   int64  `gorm:"column:order_key;not null;default:0;index:identities_order,priority:1"`
	CreatedAt  int64  `gorm:"column:created_at;not null"`
	UpdatedAt  int64  `gorm:"column:updated_at;not null"`
	Data       string `gorm:"column:data;type:text;not null"`
}

func (identitySchema) TableName() string { return "identities" }

type forwardingRuleSchema struct {
	ID          string `gorm:"column:id;type:text;primaryKey;index:forwarding_rules_order,priority:3"`
	Label       string `gorm:"column:label;type:text;not null;index:forwarding_rules_order,priority:2,collate:nocase"`
	Type        string `gorm:"column:type;type:text;not null"`
	HostID      string `gorm:"column:host_id;type:text;not null;default:''"`
	BindAddress string `gorm:"column:bind_address;type:text;not null;default:''"`
	LocalPort   int64  `gorm:"column:local_port;not null;default:0"`
	OrderKey    int64  `gorm:"column:order_key;not null;default:0;index:forwarding_rules_order,priority:1"`
	CreatedAt   int64  `gorm:"column:created_at;not null;default:0"`
	UpdatedAt   int64  `gorm:"column:updated_at;not null;default:0"`
	Data        string `gorm:"column:data;type:text;not null"`
}

func (forwardingRuleSchema) TableName() string { return "forwarding_rules" }

type workspaceLayoutSchema struct {
	ID        int64  `gorm:"column:id;primaryKey;check:id = 1"`
	Version   int64  `gorm:"column:version;not null"`
	Data      string `gorm:"column:data;type:text;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
}

func (workspaceLayoutSchema) TableName() string { return "workspace_layout" }

type hostGroupSchema struct {
	Path             string `gorm:"column:path;type:text;primaryKey;index:host_groups_order,priority:2,collate:nocase"`
	Notes            string `gorm:"column:notes;type:text;not null;default:''"`
	Username         string `gorm:"column:username;type:text;not null;default:''"`
	Port             int64  `gorm:"column:port;not null;default:0"`
	Protocol         string `gorm:"column:protocol;type:text;not null;default:''"`
	AuthMethod       string `gorm:"column:auth_method;type:text;not null;default:''"`
	IdentityID       string `gorm:"column:identity_id;type:text;not null;default:''"`
	KeyID            string `gorm:"column:key_id;type:text;not null;default:''"`
	UseSSHAgent      int64  `gorm:"column:use_ssh_agent;type:integer;not null;default:0"`
	RequiresMFA      int64  `gorm:"column:requires_mfa;type:integer;not null;default:0"`
	LegacyAlgorithms int64  `gorm:"column:legacy_algorithms;type:integer;not null;default:0"`
	SkipECDSAHostKey int64  `gorm:"column:skip_ecdsa_host_key;type:integer;not null;default:0"`
	ProxyProfileID   string `gorm:"column:proxy_profile_id;type:text;not null;default:''"`
	OrderKey         int64  `gorm:"column:order_key;not null;default:0;index:host_groups_order,priority:1"`
	CreatedAt        int64  `gorm:"column:created_at;not null"`
	UpdatedAt        int64  `gorm:"column:updated_at;not null"`
	Data             string `gorm:"column:data;type:text;not null"`
}

func (hostGroupSchema) TableName() string { return "host_groups" }

type proxyProfileSchema struct {
	ID        string `gorm:"column:id;type:text;primaryKey;index:proxy_profiles_order,priority:3"`
	Label     string `gorm:"column:label;type:text;not null;index:proxy_profiles_order,priority:2,collate:nocase"`
	Type      string `gorm:"column:type;type:text;not null;default:''"`
	Host      string `gorm:"column:host;type:text;not null;default:''"`
	Port      int64  `gorm:"column:port;not null;default:0"`
	OrderKey  int64  `gorm:"column:order_key;not null;default:0;index:proxy_profiles_order,priority:1"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
	Data      string `gorm:"column:data;type:text;not null"`
}

func (proxyProfileSchema) TableName() string { return "proxy_profiles" }

type snippetSchema struct {
	ID          string `gorm:"column:id;type:text;primaryKey;index:snippets_order,priority:3"`
	Label       string `gorm:"column:label;type:text;not null;index:snippets_order,priority:2,collate:nocase"`
	PackagePath string `gorm:"column:package_path;type:text;not null;default:'';index:snippets_package,priority:1"`
	OrderKey    int64  `gorm:"column:order_key;not null;default:0;index:snippets_order,priority:1;index:snippets_package,priority:2"`
	CreatedAt   int64  `gorm:"column:created_at;not null"`
	UpdatedAt   int64  `gorm:"column:updated_at;not null"`
	Data        string `gorm:"column:data;type:text;not null"`
}

func (snippetSchema) TableName() string { return "snippets" }

type snippetPackageSchema struct {
	Path      string `gorm:"column:path;type:text;primaryKey;index:snippet_packages_order,priority:2,collate:nocase"`
	OrderKey  int64  `gorm:"column:order_key;not null;default:0;index:snippet_packages_order,priority:1"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
	Data      string `gorm:"column:data;type:text;not null"`
}

func (snippetPackageSchema) TableName() string { return "snippet_packages" }

type noteSchema struct {
	ID        string `gorm:"column:id;type:text;primaryKey;index:vault_notes_order,priority:3"`
	Title     string `gorm:"column:title;type:text;not null;index:vault_notes_order,priority:2,collate:nocase"`
	GroupPath string `gorm:"column:group_path;type:text;not null;default:'';index:vault_notes_group,priority:1"`
	OrderKey  int64  `gorm:"column:order_key;not null;default:0;index:vault_notes_order,priority:1;index:vault_notes_group,priority:2"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
	Data      string `gorm:"column:data;type:text;not null"`
}

func (noteSchema) TableName() string { return "vault_notes" }

type noteGroupSchema struct {
	Path      string `gorm:"column:path;type:text;primaryKey;index:note_groups_order,priority:2,collate:nocase"`
	OrderKey  int64  `gorm:"column:order_key;not null;default:0;index:note_groups_order,priority:1"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
	Data      string `gorm:"column:data;type:text;not null"`
}

func (noteGroupSchema) TableName() string { return "note_groups" }

type connectionLogSchema struct {
	ID        string `gorm:"column:id;type:text;primaryKey;index:connection_logs_started,priority:2,sort:desc"`
	SessionID string `gorm:"column:session_id;type:text;not null;default:''"`
	HostID    string `gorm:"column:host_id;type:text;not null;default:'';index:connection_logs_host,priority:1"`
	HostLabel string `gorm:"column:host_label;type:text;not null;default:''"`
	Hostname  string `gorm:"column:hostname;type:text;not null;default:''"`
	Username  string `gorm:"column:username;type:text;not null;default:''"`
	Kind      string `gorm:"column:kind;type:text;not null;default:''"`
	Status    string `gorm:"column:status;type:text;not null"`
	Error     string `gorm:"column:error;type:text;not null;default:''"`
	StartedAt int64  `gorm:"column:started_at;not null;index:connection_logs_started,priority:1,sort:desc;index:connection_logs_host,priority:2,sort:desc"`
	EndedAt   int64  `gorm:"column:ended_at;not null;default:0"`
	Data      string `gorm:"column:data;type:text;not null"`
}

func (connectionLogSchema) TableName() string { return "connection_logs" }

type historyEntrySchema struct {
	ID         string `gorm:"column:id;type:text;primaryKey;index:history_entries_recorded,priority:2,sort:desc;index:history_entries_host,priority:3,sort:desc;index:history_entries_scope,priority:3,sort:desc"`
	SessionID  string `gorm:"column:session_id;type:text;not null;default:''"`
	HostID     string `gorm:"column:host_id;type:text;not null;default:'';index:history_entries_host,priority:1"`
	HostLabel  string `gorm:"column:host_label;type:text;not null;default:''"`
	Scope      string `gorm:"column:scope;type:text;not null;default:'';index:history_entries_scope,priority:1"`
	Command    string `gorm:"column:command;type:text;not null"`
	Source     string `gorm:"column:source;type:text;not null;default:''"`
	RecordedAt int64  `gorm:"column:recorded_at;not null;index:history_entries_recorded,priority:1,sort:desc;index:history_entries_host,priority:2,sort:desc;index:history_entries_scope,priority:2,sort:desc"`
	Data       string `gorm:"column:data;type:text;not null"`
}

func (historyEntrySchema) TableName() string { return "history_entries" }

var schemaModels = []any{
	&metaSchema{},
	&hostSchema{},
	&cryptoKeySchema{},
	&secretValueSchema{},
	&knownHostSchema{},
	&sshKeySchema{},
	&identitySchema{},
	&forwardingRuleSchema{},
	&workspaceLayoutSchema{},
	&hostGroupSchema{},
	&proxyProfileSchema{},
	&snippetSchema{},
	&snippetPackageSchema{},
	&noteSchema{},
	&noteGroupSchema{},
	&connectionLogSchema{},
	&historyEntrySchema{},
}
