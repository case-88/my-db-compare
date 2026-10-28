package types_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/case-88/my-db-compare/internal/testutil"
	"github.com/case-88/my-db-compare/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestSQLLiteralRoundTripMySQL(t *testing.T) {
	conn, schema := testutil.MySQL(t)
	testutil.Exec(t, conn, "CREATE TABLE "+schema+".literals (id INT PRIMARY KEY, value VARBINARY(100))")
	for _, mode := range []string{"", "NO_BACKSLASH_ESCAPES"} {
		session, err := conn.DB.Conn(context.Background())
		require.NoError(t, err)
		_, err = session.ExecContext(context.Background(), "SET SESSION sql_mode=?", mode)
		require.NoError(t, err)
		for i, value := range []interface{}{"abc\\", "O'Reilly", "a\x00b\n\x1a", "한글", []byte{0xff, 0, 0x5c, 0x27}, ""} {
			_, err = session.ExecContext(context.Background(), "DELETE FROM "+schema+".literals")
			require.NoError(t, err)
			q := types.BuildInsertQuery(schema+".literals", types.Row{"id": i, "value": value})
			_, err = session.ExecContext(context.Background(), q)
			require.NoError(t, err, "mode=%s query=%s", mode, q)
			var got []byte
			err = session.QueryRowContext(context.Background(), "SELECT value FROM "+schema+".literals").Scan(&got)
			require.NoError(t, err)
			var want []byte
			if b, ok := value.([]byte); ok {
				want = b
			} else {
				want = []byte(value.(string))
			}
			require.Equal(t, hex.EncodeToString(want), hex.EncodeToString(got))
			q = types.BuildUpdateQuery(schema+".literals", map[string]interface{}{"id": i}, types.Row{"value": value}, []string{"value"})
			_, err = session.ExecContext(context.Background(), q)
			require.NoError(t, err)
			q = types.BuildDeleteQuery(schema+".literals", map[string]interface{}{"id": i, "value": value})
			res, err := session.ExecContext(context.Background(), q)
			require.NoError(t, err)
			n, err := res.RowsAffected()
			require.NoError(t, err)
			require.EqualValues(t, 1, n, fmt.Sprint(value))
		}
		session.Close()
	}
}

func TestSQLTextAndJSONLiteralsMySQL(t *testing.T) {
	conn, schema := testutil.MySQL(t)
	testutil.Exec(t, conn, "CREATE TABLE "+schema+".texts (id INT PRIMARY KEY, txt TEXT CHARACTER SET utf8mb4, doc JSON)")
	want := "한글\\value"
	document, err := json.Marshal(map[string]string{"text": want})
	require.NoError(t, err)
	query := types.BuildInsertQuery(schema+".texts", types.Row{"id": 1, "txt": []byte(want), "doc": document})
	testutil.Exec(t, conn, query)
	var text string
	var gotJSON []byte
	require.NoError(t, conn.DB.QueryRow("SELECT txt,doc FROM "+schema+".texts").Scan(&text, &gotJSON))
	require.Equal(t, want, text)
	var got map[string]string
	require.NoError(t, json.Unmarshal(gotJSON, &got))
	require.Equal(t, want, got["text"])
}
