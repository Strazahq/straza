package config

// Capture section: where transcript bodies live. Types, validation.
// The env faces stay in config.go's applyEnv.

import (
	"fmt"
)

// Capture is the platform half of conversation capture. The policy
// plane decides WHO is captured (spec/policyset §6); this block decides
// WHERE transcript bodies live.
type Capture struct {
	BodyStore BodyStoreCfg `yaml:"bodyStore"`
}

// BodyStoreCfg selects the transcript body store. Empty type = bodies stay
// inline in the relational store (the default, correct below ~10k agents).
// Type "s3" (enterprise only) sends bodies content-addressed to an
// S3-compatible bucket: dedup + integrity by key, retention = the bucket's
// lifecycle rule, residency = the bucket's region. Reads stay transparent:
// console/ctl serve identical JSON wherever the bytes live.
type BodyStoreCfg struct {
	Type          string `yaml:"type"` // "" (inline) | "s3"
	Endpoint      string `yaml:"endpoint"`
	Bucket        string `yaml:"bucket"`
	Prefix        string `yaml:"prefix"`
	Region        string `yaml:"region"`
	AccessKey     string `yaml:"accessKey"`
	AccessKeyFile string `yaml:"accessKeyFile"` // wins over AccessKey
	SecretKey     string `yaml:"secretKey"`
	SecretKeyFile string `yaml:"secretKeyFile"` // wins over SecretKey
	// DisableSSL turns off TLS to the endpoint (in-cluster MinIO on a
	// private network); the default is TLS on.
	DisableSSL bool `yaml:"disableSSL"`
}

// validate holds the capture section's checks; the profile is passed in
// because the s3 body store is enterprise-only.
func (c Capture) validate(profile string) error {
	switch c.BodyStore.Type {
	case "":
	case "s3":
		if profile != ProfileEnterprise {
			return fmt.Errorf("capture.bodyStore type s3 is enterprise-only; standalone keeps bodies inline")
		}
		if c.BodyStore.Endpoint == "" || c.BodyStore.Bucket == "" {
			return fmt.Errorf("capture.bodyStore type s3 requires endpoint and bucket")
		}
		if err := checkHostPort("capture.bodyStore.endpoint", c.BodyStore.Endpoint); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown capture.bodyStore.type %q: expected \"\" (inline) or s3", c.BodyStore.Type)
	}
	return nil
}

// applyEnvCapture binds the capture section's env faces; applyEnv
// (config.go) sequences the binders, and each face writes a field no other
// face writes.
func applyEnvCapture(cfg *Config, getenv func(string) string) {
	set := envSet(getenv)
	set("STRAZA_CAPTURE_BODYSTORE_ACCESS_KEY", func(v string) { cfg.Capture.BodyStore.AccessKey = v })
	set("STRAZA_CAPTURE_BODYSTORE_SECRET_KEY", func(v string) { cfg.Capture.BodyStore.SecretKey = v })
	// File siblings: see the Slack *_FILE comment in applyEnvApproval
	// (config.go) for why these exist.
	set("STRAZA_CAPTURE_BODYSTORE_ACCESS_KEY_FILE", func(v string) { cfg.Capture.BodyStore.AccessKeyFile = v })
	set("STRAZA_CAPTURE_BODYSTORE_SECRET_KEY_FILE", func(v string) { cfg.Capture.BodyStore.SecretKeyFile = v })
}
