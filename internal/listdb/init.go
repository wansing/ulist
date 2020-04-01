//go:generate go run assets_gen.go
package listdb

import (
	"log"

	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

const BatchLimit = 1000 // for database operations
const BounceAddressSuffix = "+bounces"

var gdprLogger util.Logger
var spoolDir string
var webUrl string

var Mta mailutil.MTA = mailutil.Sendmail{}

func init() {
	log.SetFlags(0) // no log prefixes required, systemd-journald adds them
}
