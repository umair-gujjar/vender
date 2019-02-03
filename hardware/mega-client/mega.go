package mega

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brian-armstrong/gpio"
	"github.com/juju/errors"
	"github.com/temoto/alive"
	"github.com/temoto/vender/crc"
	"github.com/temoto/vender/hardware/i2c"
	"github.com/temoto/vender/helpers/msync"
)

const modName string = "mega-client"

type Client struct {
	bus       i2c.I2CBus
	addr      byte
	pin       uint
	twiCh     chan Packet
	respCh    chan Packet
	strayCh   chan Packet
	refcount  int32
	alive     *alive.Alive
	serialize sync.Mutex
}

func NewClient(busNo byte, addr byte, pin uint) (*Client, error) {
	self := &Client{
		addr:    addr,
		bus:     i2c.NewI2CBus(busNo),
		pin:     pin,
		alive:   alive.NewAlive(),
		respCh:  make(chan Packet, 16),
		strayCh: make(chan Packet, 16),
		twiCh:   make(chan Packet, 16),
	}
	go self.reader()
	return self, nil
}

func (self *Client) Close() error {
	self.alive.Stop()
	self.alive.Wait()
	return errors.NotImplementedf("")
}

func (self *Client) IncRef(debug string) {
	log.Printf("%s incref by %s", modName, debug)
	atomic.AddInt32(&self.refcount, 1)
}
func (self *Client) DecRef(debug string) error {
	log.Printf("%s decref by %s", modName, debug)
	new := atomic.AddInt32(&self.refcount, -1)
	switch {
	case new > 0:
		return nil
	case new == 0:
		return self.Close()
	}
	panic(fmt.Sprintf("code error %s decref<0 debug=%s", modName, debug))
}

// TODO make it private, used by mega-cli
func (self *Client) RawRead(b []byte) error {
	n, err := self.bus.ReadBytesAt(self.addr, b)
	if err != nil {
		log.Printf("%s RawRead addr=%02x error=%v", modName, self.addr, err)
		return err
	}
	log.Printf("%s RawRead addr=%02x n=%v buf=%02x", modName, self.addr, n, b)
	return nil
}

// TODO make it private, used by mega-cli
func (self *Client) RawWrite(b []byte) error {
	err := self.bus.WriteBytesAt(self.addr, b)
	log.Printf("%s RawWrite addr=%02x buf=%02x err=%v", modName, self.addr, b, err)
	return err
}

// TODO FIXME WIP
func (self *Client) DoPoll() error {
	return self.Do(&Tx{Rq: []byte{byte(Command_Poll)}})
}

type Tx struct {
	Rq []byte
	Rs []byte
	Ps []Packet
	E  error
	w  msync.Signal
}

func (self *Client) Do(tx *Tx) error {
	self.serialize.Lock()
	defer self.serialize.Unlock()

	bufOut := make([]byte, COMMAND_MAX_LENGTH)
	plen := len(tx.Rq) + 2
	bufOut[0] = byte(plen)
	copy(bufOut[1:], tx.Rq)
	bufOut[plen-1] = crc.CRC8_p93_n(0, bufOut[:plen-1])
	_ = self.RawWrite(bufOut[:plen])

	select {
	case p := <-self.respCh:
		tx.Ps = append(tx.Ps, p)
		log.Printf("Do response %s", p.String())
	case <-time.After(500 * time.Millisecond):
		return errors.Timeoutf("omg")
	}
	return nil
}

func (self *Client) reader() {
	stopch := self.alive.StopChan()
	bufIn := make([]byte, RESPONSE_MAX_LENGTH)

	pinWatch := gpio.NewWatcher()
	pinWatch.AddPinWithEdgeAndLogic(self.pin, gpio.EdgeRising, gpio.ActiveHigh)
	defer pinWatch.Close()

	for self.alive.IsRunning() {
		select {
		case <-pinWatch.Notification:
			log.Printf("pin edge")
			err := self.RawRead(bufIn)
			if err != nil {
				log.Printf("%s pin read=%02x error=%v", modName, self.addr, err)
				break
			}
			err = ParseResponse(bufIn, func(p Packet) {
				log.Printf("- debug packet=%s %s", p.Hex(), p.String())
				switch p.Header {
				case Response_TWI:
					self.twiCh <- p
				case Response_UART_Read_Unexpected:
					self.strayCh <- p
				default:
					self.respCh <- p
				}
			})
			if err != nil {
				log.Printf("pin read=%02x parse error=%v", bufIn, err)
				break
			}

		case <-stopch:
			return
		}
	}
}
