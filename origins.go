package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ApolloF/syncer/internal/backup"
	"github.com/ApolloF/syncer/internal/meta"
	"github.com/ApolloF/syncer/internal/syncthing"
)

func init() { backup.FileOrigin = fileOrigin }

// originNames caches the Syncthing client and the PCs' names for
// fileOrigin, which a backup calls once per file it copies.
var originNames struct {
	sync.Mutex
	c     *syncthing.Client
	names map[string]string // short device id -> PC name
	at    time.Time
}

// fileOrigin names the PC that last changed a synced folder's file, as
// Syncthing's index has it ("" when unknown).
func fileOrigin(id, rel string) string {
	o := &originNames
	o.Lock()
	defer o.Unlock()
	if o.c == nil || time.Since(o.at) > time.Minute {
		c, err := syncthing.New()
		if err != nil {
			return ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		st, err := c.Status(ctx)
		cancel()
		if err != nil {
			return ""
		}
		o.c, o.at, o.names = c, time.Now(), map[string]string{}
		for dev, name := range meta.Peers() {
			if len(dev) >= 7 && name != "" {
				o.names[dev[:7]] = name
			}
		}
		if len(st.MyID) >= 7 {
			o.names[st.MyID[:7]] = backup.HostName() // as this PC's backups name it
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	by, err := o.c.ModifiedBy(ctx, id, rel)
	if err != nil || by == "" {
		return ""
	}
	return o.names[strings.ToUpper(by)]
}
