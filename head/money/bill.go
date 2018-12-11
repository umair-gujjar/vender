package money

import (
	"context"
	"log"

	"github.com/temoto/alive"
	"github.com/temoto/vender/hardware/mdb"
	"github.com/temoto/vender/hardware/mdb/bill"
	"github.com/temoto/vender/hardware/money"
)

type BillState struct {
	alive *alive.Alive
	// TODO escrow currency.NominalGroup
	hw bill.BillValidator
}

func (self *BillState) Init(ctx context.Context, parent *MoneySystem, m mdb.Mdber) error {
	log.Printf("head/money/bill init")
	self.alive = alive.NewAlive()
	pch := make(chan money.PollResult, 2)
	if err := self.hw.Init(ctx, m); err != nil {
		return err
	}
	self.alive.Add(2)
	go self.hw.Run(ctx, self.alive, pch)
	go self.pollResultLoop(ctx, parent, pch)
	return nil
}

func (self *BillState) Stop(ctx context.Context) {
	self.alive.Stop()
	self.alive.Wait()
}

func (self *BillState) pollResultLoop(ctx context.Context, m *MoneySystem, pch <-chan money.PollResult) {
	defer self.alive.Done()

	const logPrefix = "head/money/bill"
	h := func(m *MoneySystem, pr *money.PollResult, pi money.PollItem) bool {
		switch pi.Status {
		case money.StatusRejected:
		case money.StatusDisabled:
			// TODO telemetry
		case money.StatusEscrow:
			// TODO self.hw.EscrowAccept / Reject
		case money.StatusWasReset:
			self.hw.DoIniter.Do(ctx)
		case money.StatusBusy:
		default:
			return false
		}
		return true
	}
	pollResultLoop(ctx, m, pch, h, self.hw.NewRestarter(), logPrefix)
}
