package clients

import (
	"fmt"
	"testing"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetClientsByUUIDsQueryCountAndMissing(t *testing.T) {
	cases := []struct {
		name    string
		count   int
		queries int
	}{
		{name: "100", count: 100, queries: 1},
		{name: "500", count: 500, queries: 2},
		{name: "1000", count: 1000, queries: 3},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db := newClientTestDB(t, "uuid-lookup-"+test.name)
			rows := make([]models.Client, 0, test.count)
			uuids := make([]string, 0, test.count+1)
			for i := 0; i < test.count; i++ {
				uuid := fmt.Sprintf("node-%s-%04d", test.name, i)
				rows = append(rows, models.Client{
					UUID:                 uuid,
					Token:                fmt.Sprintf("token-%s-%04d", test.name, i),
					RemoteProtocol:       2,
					RemoteControlEnabled: true,
				})
				uuids = append(uuids, uuid)
			}
			for i := 0; i < len(rows); i += uuidQueryChunkSize {
				end := i + uuidQueryChunkSize
				if end > len(rows) {
					end = len(rows)
				}
				require.NoError(t, db.Create(rows[i:end]).Error)
			}
			uuids = append(uuids, "missing-"+test.name)

			var queries int
			callbackName := "test:count_uuid_lookup_" + test.name
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, func(*gorm.DB) {
				queries++
			}))
			t.Cleanup(func() { db.Callback().Query().Remove(callbackName) })

			found, err := getClientsByUUIDs(db, append(uuids, uuids[0]))
			require.NoError(t, err)
			require.Equal(t, test.count, len(found))
			_, missing := found["missing-"+test.name]
			require.False(t, missing)
			require.Equal(t, test.queries, queries)
			require.Equal(t, 2, found[uuids[0]].RemoteProtocol)
			require.True(t, found[uuids[0]].RemoteControlEnabled)
		})
	}
}
