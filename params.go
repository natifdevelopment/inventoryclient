package inventoryclient

import (
	"time"

	"github.com/google/uuid"
)

// InventoryBalance is the minimal response shape returned by stock-api
// for balance queries. Only the Balance field is consumed by callers.
type InventoryBalance struct {
	Balance         float64    `json:"balance"`
	TOrganizationId uuid.UUID  `json:"organizationId"`
	TglTransaksi    time.Time  `json:"tglTransaksi"`
	Amount          float64    `json:"amount"`
	Type            string     `json:"type"`
	TRefId          *uuid.UUID `json:"refId"`
	TTargetId       *uuid.UUID `json:"targetId"`
	PageCode        string     `json:"pageCode"`
}

// InventoryType mirrors models.InventoryType in stock-api.
type InventoryType string

const (
	InventoryTypePenerimaan InventoryType = "penerimaan"
	InventoryTypePemakaian  InventoryType = "pemakaian"
	InventoryTypeOpname     InventoryType = "opname"
	InventoryTypeStokAwal   InventoryType = "stok_awal"
)

// UpsertInventoryParams holds the parameters for AddInventory and UpdateInventory.
type UpsertInventoryParams struct {
	OrganizationId  uuid.UUID
	TransactionDate time.Time
	Amount          float64
	TransactionType InventoryType
	TargetId        *uuid.UUID
	ReferenceId     uuid.UUID
}

// DeleteInventoryParams holds the parameters for DeleteInventory.
type DeleteInventoryParams struct {
	OrganizationId uuid.UUID
	TargetId       *uuid.UUID
	ReferenceId    uuid.UUID
}
