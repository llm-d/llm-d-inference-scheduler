package config

import (
	"github.com/redis/go-redis/v9"
	"strings"

	//"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestURLParse(t *testing.T) {
	urlInst := "bd73f810-0eea-46a9-b330-84c9bc36d00d.blrrvkdw0thh68l98t20.private.databases.appdomain.cloud:31394"
	if !(strings.HasPrefix(urlInst, "redis://") || strings.HasPrefix(urlInst, "rediss://") || strings.HasPrefix(urlInst, "unix://")) {
		urlInst = "redis://" + urlInst
	}
	t.Log(urlInst)
	opr, err := redis.ParseURL(urlInst)
	assert.NoError(t, err)
	t.Log(opr.Addr)
	t.Log(opr.Username)
	t.Log(opr.Password)
	t.Log(opr.DB)
	t.Log(opr.TLSConfig)
}
