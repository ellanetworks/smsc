package cluster

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
)

func BenchmarkReplicatedWrites(b *testing.B) {
	for _, where := range []string{"leader", "follower"} {
		b.Run(where, func(b *testing.B) {
			c := newCluster(b, 3)
			target := c.leader()

			if where == "follower" {
				target = c.follower()
			}

			var seq atomic.Int64

			b.SetParallelism(4)
			b.ResetTimer()

			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					i := seq.Add(1)
					now := time.Now()
					msisdn := fmt.Sprintf("1555%07d", i)
					m := db.NewMessage{
						Originator: db.Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1},
						Recipient:  db.Address{Digits: msisdn, TypeOfNumber: 1, NumberingPlan: 1},
						MSISDN:     msisdn, Origin: db.OriginMobile, TPDU: []byte{1, 2, 3},
						SubmittedAt: now, ExpiresAt: now.Add(time.Hour),
					}

					if _, err := target.db.CreateMessages(context.Background(), []db.NewMessage{m}); err != nil {
						b.Error(err)
						return
					}
				}
			})

			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "writes/s")
		})
	}
}
