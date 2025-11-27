package consts

const (
	ProxyMetaFieldPlatformName  = "platform_name"
	ProxyMetaFieldPlatformType  = "platform_type"
	ProxyMetaFieldOdigosVersion = "odigos_version"
	ProxyMetaFieldConnectedAt   = "connected_at" // seconds since epoch (we store secs; resolver multiplies to ms)
	ProxyMetaFieldLastSeenAt    = "last_seen_at" // seconds since epoch (we store secs; resolver multiplies to ms)
)


