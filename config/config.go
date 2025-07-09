package config

type Config struct {
	DiscordAuth   DiscordAuth         `yaml:"discord_auth" validate:"required"`
	Sites         Sites               `yaml:"sites" validate:"required"`
	Servers       Servers             `yaml:"servers" validate:"required"`
	Meta          Meta                `yaml:"meta" validate:"required"`
	ObjectStorage ObjectStorageConfig `yaml:"object_storage" validate:"required"`
	BasePorts     BasePorts           `yaml:"base_ports" validate:"required"`
}

type DiscordAuth struct {
	Token            string   `yaml:"token" comment:"Discord bot token" validate:"required"`
	ClientID         string   `yaml:"client_id" default:"849331145862283275" comment:"Discord Client ID" validate:"required"`
	ClientSecret     string   `yaml:"client_secret" comment:"Discord Client Secret" validate:"required"`
	AllowedRedirects []string `yaml:"allowed_redirects" default:"http://localhost:3000/auth" validate:"required"`
	RootUsers        []string `yaml:"root_users" default:"123456789012345678" comment:"List of user IDs that are considered root users and can access all endpoints" validate:"required"`
}

type Sites struct {
	Frontend string `yaml:"frontend" default:"https://antiraid.xyz" comment:"Frontend URL" validate:"required"`
	API      string `yaml:"api" default:"https://splashtail.antiraid.xyz" comment:"API URL" validate:"required"`
	Docs     string `yaml:"docs" default:"https://docs.antiraid.xyz" comment:"Docs URL" validate:"required"`
}

type Servers struct {
	Main string `yaml:"main" default:"1064135068928454766" comment:"Main Server ID" validate:"required"`
}

type Meta struct {
	WebDisableRatelimits bool   `yaml:"web_disable_ratelimits" comment:"Disable ratelimits for the web server"`
	PostgresURL          string `yaml:"postgres_url" default:"postgresql:///antiraid" comment:"Postgres URL" validate:"required"`
	RedisURL             string `yaml:"redis_url" default:"redis://localhost:6379" comment:"Redis URL" validate:"required"`
	Port                 int    `yaml:"port" default:":8081" comment:"Port to run the server on" validate:"required"`
	CDNPath              string `yaml:"cdn_path" default:"/failuremgmt/cdn/antiraid" comment:"CDN Path" validate:"required"`
	Proxy                string `yaml:"proxy" default:"http://127.0.0.1:3221" comment:"Proxy URL" validate:"required"`
	SupportServerInvite  string `yaml:"support_server_invite" comment:"Discord Support Server Link" default:"https://discord.gg/u78NFAXm" validate:"required"`
	SandwichHttpApi      string `yaml:"sandwich_http_api" comment:"(optional) Sandwich HTTP API" default:"http://127.0.0.1:29334" validate:"required"`
}

// Some data such as backups can get quite large.
// These are stored on a S3-like bucket such as DigitalOcean spaces
type ObjectStorageConfig struct {
	Type        string `yaml:"type" comment:"Must be one of s3-like or local" validate:"required" oneof:"s3-like local"`
	BasePath    string `yaml:"base_path" comment:"If s3-like, this should be the base of the bucket. Otherwise, should be the path to the location to store to"`
	Endpoint    string `yaml:"endpoint" comment:"Only for s3-like, this should be the endpoint to the bucket."`
	CdnEndpoint string `yaml:"cdn_endpoint" comment:"Only for s3-like (and DigitalOcean mainly), this should be the CDN endpoint to the bucket."`
	Secure      bool   `yaml:"secure" comment:"Only for s3-like, this should be whether or not to use a secure connection to the bucket."`
	CdnSecure   bool   `yaml:"cdn_secure" comment:"Only for s3-like, this should be whether or not to use a secure connection to the CDN."`
	AccessKey   string `yaml:"access_key" comment:"Only for s3-like, this should be the access key to the bucket."`
	SecretKey   string `yaml:"secret_key" comment:"Only for s3-like, this should be the secret key to the bucket."`
}

type BasePorts struct {
	JobserverBaseAddr  string `yaml:"jobserver_base_addr" default:"http://localhost" comment:"Jobserver Base Address" validate:"required"`
	JobserverBindAddr  string `yaml:"jobserver_bind_addr" default:"127.0.0.1" comment:"Jobserver Bind Address" validate:"required"`
	Jobserver          int    `yaml:"jobserver" default:"30000" comment:"Jobserver Base Port" validate:"required"`
	TemplateWorkerAddr string `yaml:"template_worker_addr" default:"http://localhost" comment:"Template Worker Address" validate:"required"`
	TemplateWorkerPort int    `yaml:"template_worker_port" default:"60000" comment:"Template Worker Port" validate:"required"`
}
