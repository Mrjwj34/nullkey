package audit

import (
	"time"

	nkid "github.com/Mrjwj34/nullkey/internal/id"
)

func eventID() string { return nkid.New(nkid.Event) }

func now() time.Time { return time.Now().UTC() }
