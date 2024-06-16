package db

import (
	"fmt"
	"strings"

	"github.com/ethpandaops/dora/dbtypes"
	"github.com/jmoiron/sqlx"
)

func InsertMevBlocks(mevBlocks []*dbtypes.MevBlock, tx *sqlx.Tx) error {
	var sql strings.Builder
	fmt.Fprint(&sql,
		EngineQuery(map[dbtypes.DBEngineType]string{
			dbtypes.DBEnginePgsql:  "INSERT INTO mev_blocks ",
			dbtypes.DBEngineSqlite: "INSERT OR REPLACE INTO mev_blocks ",
		}),
		"(slot_number, block_hash, block_number, builder_pubkey, proposer_index, proposed, seenby_relays, fee_recipient, tx_count, gas_used, block_value, block_value_gwei)",
		" VALUES ",
	)
	argIdx := 0
	fieldCount := 12

	args := make([]any, len(mevBlocks)*fieldCount)
	for i, mevBlock := range mevBlocks {
		if i > 0 {
			fmt.Fprintf(&sql, ", ")
		}
		fmt.Fprintf(&sql, "(")
		for f := 0; f < fieldCount; f++ {
			if f > 0 {
				fmt.Fprintf(&sql, ", ")
			}
			fmt.Fprintf(&sql, "$%v", argIdx+f+1)

		}
		fmt.Fprintf(&sql, ")")

		args[argIdx+0] = mevBlock.SlotNumber
		args[argIdx+1] = mevBlock.BlockHash
		args[argIdx+2] = mevBlock.BlockNumber
		args[argIdx+3] = mevBlock.BuilderPubkey
		args[argIdx+4] = mevBlock.ProposerIndex
		args[argIdx+5] = mevBlock.Proposed
		args[argIdx+6] = mevBlock.SeenbyRelays
		args[argIdx+7] = mevBlock.FeeRecipient
		args[argIdx+8] = mevBlock.TxCount
		args[argIdx+9] = mevBlock.GasUsed
		args[argIdx+10] = mevBlock.BlockValue
		args[argIdx+11] = mevBlock.BlockValueGwei
		argIdx += fieldCount
	}
	fmt.Fprint(&sql, EngineQuery(map[dbtypes.DBEngineType]string{
		dbtypes.DBEnginePgsql:  " ON CONFLICT (block_hash) DO UPDATE SET proposed = excluded.proposed, seenby_relays = excluded.seenby_relays",
		dbtypes.DBEngineSqlite: "",
	}))

	_, err := tx.Exec(sql.String(), args...)
	if err != nil {
		return err
	}
	return nil
}

func GetHighestMevBlockSlotByRelay(relayId uint8) (uint64, error) {
	highestSlot := uint64(0)
	err := ReaderDb.Get(&highestSlot, `
	SELECT
		MAX(slot_number)
	FROM mev_blocks
	WHERE (seenby_relays & $1) != 0
	`, uint64(1)<<relayId)
	if err != nil {
		return 0, err
	}
	return highestSlot, nil
}

func GetMevBlocksFiltered(offset uint64, limit uint32, filter *dbtypes.MevBlockFilter) ([]*dbtypes.MevBlock, uint64, error) {
	var sql strings.Builder
	args := []any{}
	fmt.Fprint(&sql, `
	WITH cte AS (
		SELECT
			slot_number, block_hash, block_number, builder_pubkey, proposer_index, proposed, seenby_relays, fee_recipient, tx_count, gas_used, block_value, block_value_gwei
		FROM mev_blocks
	`)

	if filter.ProposerName != "" {
		fmt.Fprint(&sql, `
		LEFT JOIN validator_names ON validator_names."index" = mev_blocks.proposer_index 
		`)
	}

	filterOp := "WHERE"
	if filter.MinSlot > 0 {
		args = append(args, filter.MinSlot)
		fmt.Fprintf(&sql, " %v slot_number >= $%v", filterOp, len(args))
		filterOp = "AND"
	}
	if filter.MaxSlot > 0 {
		args = append(args, filter.MaxSlot)
		fmt.Fprintf(&sql, " %v slot_number <= $%v", filterOp, len(args))
		filterOp = "AND"
	}
	if filter.MinIndex > 0 {
		args = append(args, filter.MinIndex)
		fmt.Fprintf(&sql, " %v proposer_index >= $%v", filterOp, len(args))
		filterOp = "AND"
	}
	if filter.MaxIndex > 0 {
		args = append(args, filter.MaxIndex)
		fmt.Fprintf(&sql, " %v proposer_index <= $%v", filterOp, len(args))
		filterOp = "AND"
	}
	if filter.Proposed > 0 {
		args = append(args, filter.Proposed)
		fmt.Fprintf(&sql, " %v proposed = $%v", filterOp, len(args))
		filterOp = "AND"
	}
	if len(filter.BuilderPubkey) > 0 {
		args = append(args, filter.BuilderPubkey)
		fmt.Fprintf(&sql, " %v builder_pubkey = $%v", filterOp, len(args))
		filterOp = "AND"
	}
	if len(filter.MevRelay) > 0 {
		seenbyPattern := uint64(0)
		for _, relayId := range filter.MevRelay {
			if relayId > 63 {
				continue
			}
			seenbyPattern |= uint64(1) << relayId
		}
		args = append(args, seenbyPattern)
		fmt.Fprintf(&sql, " %v (seenby_relays & $%v) != 0", filterOp, len(args))
		filterOp = "AND"
	}
	if filter.ProposerName != "" {
		args = append(args, "%"+filter.ProposerName+"%")
		fmt.Fprintf(&sql, " %v ", filterOp)
		fmt.Fprintf(&sql, EngineQuery(map[dbtypes.DBEngineType]string{
			dbtypes.DBEnginePgsql:  ` validator_names.name ilike $%v `,
			dbtypes.DBEngineSqlite: ` validator_names.name LIKE $%v `,
		}), len(args))

		filterOp = "AND"
	}

	args = append(args, limit)
	fmt.Fprintf(&sql, `) 
	SELECT 
		count(*) AS slot_number,
		null AS block_hash,
		0 AS block_number, 
		null AS builder_pubkey,
		0 AS proposer_index,
		0 AS proposed,
		0 AS seenby_relays,
		null AS fee_recipient,
		0 AS tx_count,
		0 AS gas_used,
		null AS block_value,
		0 AS block_value_gwei
	FROM cte
	UNION ALL SELECT * FROM (
	SELECT * FROM cte
	ORDER BY slot_number DESC
	LIMIT $%v 
	`, len(args))

	if offset > 0 {
		args = append(args, offset)
		fmt.Fprintf(&sql, " OFFSET $%v ", len(args))
	}
	fmt.Fprintf(&sql, ") AS t1")

	mevBlocks := []*dbtypes.MevBlock{}
	err := ReaderDb.Select(&mevBlocks, sql.String(), args...)
	if err != nil {
		logger.Errorf("Error while fetching filtered mev blocks: %v", err)
		return nil, 0, err
	}

	return mevBlocks[1:], mevBlocks[0].SlotNumber, nil
}
