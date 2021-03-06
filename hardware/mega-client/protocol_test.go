package mega

import (
	"encoding/hex"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestParseResponse(t *testing.T) {
	t.Parallel()
	type Case struct {
		name      string
		input     string
		expect    string
		expectErr string
	}
	cases := []Case{
		{"response-empty", "", "", "response empty not valid"},
		{"response-empty-valid-length", "00", "", ""},
		{"response-length-max", "f1", "", "response=f1 claims length=241 > MAX=80 not valid"},
		{"response-under-length", "01", "", "response=01 claims length=1 > buffer=0 not valid"},
		{"packet-short", "0101", "", "packet=01 claims length=1 < min=3 not valid"},
		{"packet-under-length", "0103", "", "packet=03 claims length=3 > buffer=1 not valid"},
		{"packet-corrupted", "030300ff", "", "packet=0300ff crc=ff actual=5b not valid"},
		{"packet-invalid-header", "0303005b", "", "packet=03005b header=00 not valid"},
		{"ok", "030301c8", "01", ""},
		{"ok-and-garbage", "030301c8ffffff", "01", ""},
		{"ok-and-short", "050301c804ff", "01", "packet=04ff claims length=4 > buffer=2 not valid"},
		{"debug-beebee", "060604beebee65", "04beebee", ""},
		{"mdb-started", "0303083a", "08", ""},
		{"mdb-started-and-timeout", "0703083a048b0569", "08,8b05", ""},
	}
	rand.New(rand.NewSource(time.Now().UnixNano())).Shuffle(len(cases), func(i int, j int) { cases[i], cases[j] = cases[j], cases[i] })
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			// t.Parallel()
			input, err := hex.DecodeString(c.input)
			if err != nil {
				t.Fatalf("invalid input=%s err='%v'", c.input, err)
			}
			var ps []string
			err = ParseResponse(input, func(p Packet) { ps = append(ps, p.Hex()) })
			errString := ""
			if err != nil {
				errString = err.Error()
			}
			if errString != c.expectErr {
				t.Errorf("input=%s expected err='%v' actual='%v'", c.input, c.expectErr, err)
			}
			psjoined := strings.Join(ps, ",")
			if psjoined != c.expect {
				t.Errorf("input=%s expected packets='%s' actual='%s'", c.input, c.expect, psjoined)
			}
		})
	}
}
