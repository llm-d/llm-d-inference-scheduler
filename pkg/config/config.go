// Package config provides the configuration reading abilities
// Current version read configuration from environment variables
package config

type Config struct {
	DefaultSchedulerScorers map[string]float64
	DecodeSchedulerScorers  map[string]float64
	PrefillSchedulerScorers map[string]float64
}
