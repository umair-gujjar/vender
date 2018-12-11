// Package bill incapsulates work with bill validators.
package bill

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"time"

	"github.com/temoto/alive"
	"github.com/temoto/vender/currency"
	"github.com/temoto/vender/hardware/mdb"
	"github.com/temoto/vender/hardware/money"
	"github.com/temoto/vender/head/state"
	"github.com/temoto/vender/helpers/msync"
)

const (
	billTypeCount = 16

	DelayErr  = 500 * time.Millisecond
	DelayNext = 200 * time.Millisecond
)

//go:generate stringer -type=Features -trimprefix=Feature
type Features uint32

const (
	FeatureFTL Features = 1 << iota
	FeatureRecycling
)

type BillValidator struct {
	dev mdb.Device

	// Indicates the value of the bill types 0 to 15.
	// These are final values including all scaling factors.
	billTypeCredit []currency.Nominal

	featureLevel      uint8
	supportedFeatures Features

	// Escrow capability.
	escrowCap bool

	internalScalingFactor int
	ready                 msync.Signal

	DoIniter msync.Doer
}

var (
	packetReset           = mdb.PacketFromHex("30")
	packetSetup           = mdb.PacketFromHex("31")
	packetPoll            = mdb.PacketFromHex("33")
	packetEscrowAccept    = mdb.PacketFromHex("3501")
	packetEscrowReject    = mdb.PacketFromHex("3500")
	packetStacker         = mdb.PacketFromHex("36")
	packetExpIdent        = mdb.PacketFromHex("3700")
	packetExpIdentOptions = mdb.PacketFromHex("3702")
)

var (
	ErrDefectiveMotor   = fmt.Errorf("Defective Motor")
	ErrBillRemoved      = fmt.Errorf("Bill Removed")
	ErrEscrowImpossible = fmt.Errorf("An ESCROW command was requested for a bill not in the escrow position.")
	ErrAttempts         = fmt.Errorf("Attempts")
)

func (self *BillValidator) Init(ctx context.Context, mdber mdb.Mdber) error {
	// TODO read config
	self.dev.Address = 0x30
	self.dev.Name = "billvalidator"
	self.dev.ByteOrder = binary.BigEndian
	self.dev.Mdber = mdber

	self.DoIniter = self.newIniter()

	self.billTypeCredit = make([]currency.Nominal, billTypeCount)
	self.ready = msync.NewSignal()
	// TODO maybe execute CommandReset?
	err := self.DoIniter.Do(ctx)
	return err
}

func (self *BillValidator) Run(ctx context.Context, a *alive.Alive, ch chan<- money.PollResult) {
	defer a.Done()

	stopch := a.StopChan()
	for a.IsRunning() {
		pr := self.CommandPoll()
		select {
		case ch <- pr:
		case <-stopch:
			return
		}
		select {
		case <-time.After(pr.Delay):
		case <-stopch:
			return
		}
	}
}

func (self *BillValidator) ReadyChan() <-chan msync.Nothing {
	return self.ready
}

func (self *BillValidator) newIniter() msync.Doer {
	tx := msync.NewTransaction("bill-init")
	tx.Root.
		Append(&msync.DoFunc0{F: self.CommandSetup}).
		Append(&msync.DoFunc0{F: func() error {
			if err := self.CommandExpansionIdentificationOptions(); err != nil {
				if _, ok := err.(mdb.FeatureNotSupported); ok {
					if err = self.CommandExpansionIdentification(); err != nil {
						return err
					}
				} else {
					return err
				}
			}
			return nil
		}}).
		Append(&msync.DoFunc0{F: self.CommandStacker}).
		Append(&msync.DoFunc{F: func(ctx context.Context) error {
			config := state.GetConfig(ctx)
			// TODO read enabled nominals from config
			_ = config
			return self.CommandBillType(0xffff, 0)
		}})
	return tx
}

func (self *BillValidator) NewRestarter() msync.Doer {
	tx := msync.NewTransaction("bill-restart")
	tx.Root.
		Append(&msync.DoFunc0{F: self.CommandReset}).
		Append(&msync.DoSleep{100 * time.Millisecond}).
		Append(self.newIniter())
	return tx
}

func (self *BillValidator) CommandReset() error {
	return self.dev.Tx(packetReset).E
}

func (self *BillValidator) CommandBillType(accept, escrow uint16) error {
	buf := [5]byte{0x34}
	self.dev.ByteOrder.PutUint16(buf[1:], accept)
	self.dev.ByteOrder.PutUint16(buf[3:], escrow)
	request := mdb.PacketFromBytes(buf[:])
	err := self.dev.Tx(request).E
	log.Printf("CommandBillType request=%s err=%v", request.Format(), err)
	return err
}

func (self *BillValidator) CommandSetup() error {
	const expectLength = 27
	r := self.dev.Tx(packetSetup)
	if r.E != nil {
		log.Printf("mdb request=%s err=%v", packetSetup.Format(), r.E)
		return r.E
	}
	log.Printf("setup response=(%d)%s", r.P.Len(), r.P.Format())
	bs := r.P.Bytes()
	if len(bs) < expectLength {
		return fmt.Errorf("bill validator SETUP response=%s expected %d bytes", r.P.Format(), expectLength)
	}
	scalingFactor := self.dev.ByteOrder.Uint16(bs[3:5])
	for i, sf := range bs[11:] {
		n := currency.Nominal(sf) * currency.Nominal(scalingFactor) * currency.Nominal(self.internalScalingFactor)
		log.Printf("i=%d sf=%d nominal=%s", i, sf, currency.Amount(n).Format100I())
		self.billTypeCredit[i] = n
	}
	self.escrowCap = bs[10] == 0xff
	self.featureLevel = bs[0]
	log.Printf("Bill Validator Feature Level: %d", self.featureLevel)
	log.Printf("Country / Currency Code: %x", bs[1:3])
	log.Printf("Bill Scaling Factor: %d", scalingFactor)
	log.Printf("Decimal Places: %d", bs[5])
	log.Printf("Stacker Capacity: %d", self.dev.ByteOrder.Uint16(bs[6:8]))
	log.Printf("Bill Security Levels: %016b", self.dev.ByteOrder.Uint16(bs[8:10]))
	log.Printf("Escrow/No Escrow: %t", self.escrowCap)
	log.Printf("Bill Type Credit: %x %#v", bs[11:], self.billTypeCredit)
	return nil
}

func (self *BillValidator) CommandPoll() money.PollResult {
	now := time.Now()
	r := self.dev.Tx(packetPoll)
	result := money.PollResult{Time: now, Delay: DelayNext}
	if r.E != nil {
		result.Error = r.E
		result.Delay = DelayErr
		return result
	}
	bs := r.P.Bytes()
	if len(bs) == 0 {
		self.ready.Set()
		return result
	}
	result.Items = make([]money.PollItem, len(bs))
	// log.Printf("poll response=%s", response.Format())
	for i, b := range bs {
		result.Items[i] = self.parsePollItem(b)
	}
	return result
}

func (self *BillValidator) CommandStacker() error {
	request := packetStacker
	r := self.dev.Tx(request)
	// if err != nil {
	// 	log.Printf("mdb request=%s err=%v", request.Format(), err)
	// 	return err
	// }
	log.Printf("mdb request=%s response=%s err=%v", request.Format(), r.P.Format(), r.E)
	return r.E
}

func (self *BillValidator) CommandExpansionIdentification() error {
	const expectLength = 29
	request := packetExpIdent
	r := self.dev.Tx(request)
	if r.E != nil {
		log.Printf("mdb request=%s err=%v", request.Format(), r.E)
		return r.E
	}
	log.Printf("EXPANSION IDENTIFICATION response=(%d)%s", r.P.Len(), r.P.Format())
	bs := r.P.Bytes()
	if len(bs) < expectLength {
		return fmt.Errorf("hardware/mdb/bill EXPANSION IDENTIFICATION response=%s expected %d bytes", r.P.Format(), expectLength)
	}
	log.Printf("Manufacturer Code: %x", bs[0:0+3])
	log.Printf("Serial Number: '%s'", string(bs[3:3+12]))
	log.Printf("Model #/Tuning Revision: '%s'", string(bs[15:15+12]))
	log.Printf("Software Version: %x", bs[27:27+2])
	return nil
}

func (self *BillValidator) CommandFeatureEnable(requested Features) error {
	f := requested & self.supportedFeatures
	buf := [6]byte{0x37, 0x01}
	self.dev.ByteOrder.PutUint32(buf[2:], uint32(f))
	request := mdb.PacketFromBytes(buf[:])
	err := self.dev.Tx(request).E
	if err != nil {
		log.Printf("mdb request=%s err=%v", request.Format(), err)
	}
	return err
}

func (self *BillValidator) CommandExpansionIdentificationOptions() error {
	if self.featureLevel < 2 {
		return mdb.FeatureNotSupported("EXPANSION IDENTIFICATION WITH OPTION BITS is level 2+")
	}
	const expectLength = 33
	request := packetExpIdentOptions
	r := self.dev.Tx(request)
	if r.E != nil {
		log.Printf("mdb request=%s err=%v", request.Format(), r.E)
		return r.E
	}
	log.Printf("EXPANSION IDENTIFICATION WITH OPTION BITS response=(%d)%s", r.P.Len(), r.P.Format())
	bs := r.P.Bytes()
	if len(bs) < expectLength {
		return fmt.Errorf("hardware/mdb/bill EXPANSION IDENTIFICATION WITH OPTION BITS response=%s expected %d bytes", r.P.Format(), expectLength)
	}
	self.supportedFeatures = Features(self.dev.ByteOrder.Uint32(bs[29 : 29+4]))
	log.Printf("Manufacturer Code: %x", bs[0:0+3])
	log.Printf("Serial Number: '%s'", string(bs[3:3+12]))
	log.Printf("Model #/Tuning Revision: '%s'", string(bs[15:15+12]))
	log.Printf("Software Version: %x", bs[27:27+2])
	log.Printf("Optional Features: %b", self.supportedFeatures)
	return nil
}

func (self *BillValidator) billTypeNominal(b byte) currency.Nominal {
	if b >= billTypeCount {
		log.Printf("invalid bill type: %d", b)
		return 0
	}
	return self.billTypeCredit[b]
}

func (self *BillValidator) parsePollItem(b byte) money.PollItem {
	switch b {
	case 0x01: // Defective Motor
		return money.PollItem{Status: money.StatusFatal, Error: ErrDefectiveMotor}
	case 0x02: // Sensor Problem
		return money.PollItem{Status: money.StatusFatal, Error: money.ErrSensor}
	case 0x03: // Validator Busy
		return money.PollItem{Status: money.StatusBusy}
	case 0x04: // ROM Checksum Error
		return money.PollItem{Status: money.StatusFatal, Error: money.ErrROMChecksum}
	case 0x05: // Validator Jammed
		return money.PollItem{Status: money.StatusFatal, Error: money.ErrJam}
	case 0x06: // Validator was reset
		return money.PollItem{Status: money.StatusWasReset}
	case 0x07: // Bill Removed
		return money.PollItem{Status: money.StatusError, Error: ErrBillRemoved}
	case 0x08: // Cash Box out of position
		return money.PollItem{Status: money.StatusFatal, Error: money.ErrNoStorage}
	case 0x09: // Validator Disabled
		return money.PollItem{Status: money.StatusDisabled}
	case 0x0a: // Invalid Escrow request
		return money.PollItem{Status: money.StatusError, Error: ErrEscrowImpossible}
	case 0x0b: // Bill Rejected
		return money.PollItem{Status: money.StatusRejected}
	case 0x0c: // Possible Credited Bill Removal
		return money.PollItem{Status: money.StatusError, Error: money.ErrFraud}
	}

	if b&0x8f == b { // Bill Stacked
		amount := self.billTypeNominal(b & 0xf)
		return money.PollItem{Status: money.StatusCredit, DataNominal: amount}
	}
	if b&0x9f == b { // Escrow Position
		amount := self.billTypeNominal(b & 0xf)
		log.Printf("bill escrow TODO packetEscrowAccept")
		return money.PollItem{Status: money.StatusEscrow, DataNominal: amount}
	}
	if b&0x5f == b { // Number of attempts to input a bill while validator is disabled.
		attempts := b & 0x1f
		log.Printf("Number of attempts to input a bill while validator is disabled: %d", attempts)
		return money.PollItem{Status: money.StatusInfo, Error: ErrAttempts, DataCount: attempts}
	}

	err := fmt.Errorf("parsePollItem unknown=%x", b)
	log.Print(err)
	return money.PollItem{Status: money.StatusFatal, Error: err}
}
