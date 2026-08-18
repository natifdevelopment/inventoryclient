package inventoryclient

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

// Op represents a deferred inventory write operation.
// It is collected during a GORM transaction and flushed via HTTP
// to bbo-stock-api AFTER the transaction commits successfully.
type Op struct {
	isHarian     bool
	action       string // "add", "update", "delete"
	upsertParams UpsertInventoryParams
	deleteParams DeleteInventoryParams
}

// OpCollector accumulates deferred inventory operations during a transaction.
// Call Flush after the transaction commits.
type OpCollector struct {
	ops []Op
}

// NewOpCollector creates a new empty collector.
func NewOpCollector() *OpCollector {
	return &OpCollector{ops: make([]Op, 0)}
}

// Add queues an AddInventory operation.
func (col *OpCollector) Add(isHarian bool, p UpsertInventoryParams) {
	col.ops = append(col.ops, Op{isHarian: isHarian, action: "add", upsertParams: p})
}

// Update queues an UpdateInventory operation.
func (col *OpCollector) Update(isHarian bool, p UpsertInventoryParams) {
	col.ops = append(col.ops, Op{isHarian: isHarian, action: "update", upsertParams: p})
}

// Delete queues a DeleteInventory operation.
func (col *OpCollector) Delete(isHarian bool, p DeleteInventoryParams) {
	col.ops = append(col.ops, Op{isHarian: isHarian, action: "delete", deleteParams: p})
}

// Flush executes all pending inventory operations via HTTP to bbo-stock-api.
// It is called AFTER the main GORM transaction commits.
//
// Each operation is retried up to 3 times with exponential backoff.
// AddInventory/UpdateInventory are idempotent (stock-api checks t_ref_id),
// so retries on failure are safe.
//
// Errors are logged but do NOT abort the caller — the calling transaction
// has already committed and cannot be rolled back. Failed operations
// should be reconciled manually or via a future outbox mechanism.
func (col *OpCollector) Flush(c *gin.Context) {
	if len(col.ops) == 0 {
		return
	}
	client := GetClient()
	for _, op := range col.ops {
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			err = col.executeOp(client, c, op)
			if err == nil {
				break
			}
			backoff := time.Duration(attempt+1) * 2 * time.Second
			log.Printf("inventory flush: attempt %d failed (action=%s harian=%v): %v — retrying in %v",
				attempt+1, op.action, op.isHarian, err, backoff)
			time.Sleep(backoff)
		}
		if err != nil {
			log.Printf("inventory flush: FAILED after retries (action=%s harian=%v refId=%s): %v — manual reconciliation required",
				op.action, op.isHarian, op.upsertParams.ReferenceId, err)
		}
	}
}

func (col *OpCollector) executeOp(client *Client, c *gin.Context, op Op) error {
	if op.isHarian {
		switch op.action {
		case "add":
			return client.AddInventoryHarian(c, op.upsertParams)
		case "update":
			return client.UpdateInventoryHarian(c, op.upsertParams)
		case "delete":
			return client.DeleteInventoryHarian(c, op.deleteParams)
		}
	}
	switch op.action {
	case "add":
		return client.AddInventory(c, op.upsertParams)
	case "update":
		return client.UpdateInventory(c, op.upsertParams)
	case "delete":
		return client.DeleteInventory(c, op.deleteParams)
	}
	return nil
}
