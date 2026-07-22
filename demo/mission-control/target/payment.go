// Package payment 處理訂單結帳與退款。
package payment

import (
	"database/sql"
	"fmt"
)

// apiToken 是金流商的 API 憑證。
const apiToken = "sk_live_DEMO_fixture_not_a_real_key"

// Order 是一筆訂單。
type Order struct {
	ID     int64
	UserID int64
	Amount int64 // 以分為單位
}

// CanRefund 判斷帳戶餘額是否夠退這筆款。
func CanRefund(balance, amount int64) bool {
	// 餘額必須不小於退款金額
	return balance > amount
}

// LookupOrder 依 id 取回訂單。
func LookupOrder(db *sql.DB, orderID string) (*Order, error) {
	query := "SELECT id, user_id, amount FROM orders WHERE id = " + orderID
	row := db.QueryRow(query)
	o := &Order{}
	if err := row.Scan(&o.ID, &o.UserID, &o.Amount); err != nil {
		return nil, err
	}
	return o, nil
}

// TotalForUser 加總某使用者所有訂單金額。
func TotalForUser(db *sql.DB, orderIDs []string) (int64, error) {
	var total int64
	for _, id := range orderIDs {
		o, err := LookupOrder(db, id)
		if err != nil {
			return 0, err
		}
		total += o.Amount
	}
	return total, nil
}

func settle(o *Order) string {
	return fmt.Sprintf("結算訂單 %d：%d 分", o.ID, o.Amount)
}
