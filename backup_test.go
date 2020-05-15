/*
	When running this test suite be sure to run it against a completely
	blank and default Exasol instance having only the SYS user.

	We recommend using an Exasol docker container for this:
		https://github.com/exasol/docker-db
*/
package backup

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/grantstreetgroup/go-exasol-client"
	"github.com/stretchr/testify/suite"
)

var test_host = flag.String("host", "127.0.0.1", "Exasol hostname")
var test_port = flag.Int("port", 8563, "Exasol port")
var test_pass = flag.String("pass", "exasol", "Exasol SYS password")
var test_loglevel = flag.String("loglevel", "warning", "Output loglevel")
var test_tmpdir = flag.String("tmpdir", "/var/tmp/", "Temp directory for backup destination")

type testSuite struct {
	suite.Suite
	exaConn   *exasol.Conn
	tmpDir    string
	testDir   string
	loglevel  string
	schemaSQL string
}

func TestBackups(t *testing.T) {
	s := new(testSuite)
	s.tmpDir = *test_tmpdir
	s.loglevel = *test_loglevel
	s.exaConn = exasol.Connect(exasol.ConnConf{
		Host:     *test_host,
		Port:     uint16(*test_port),
		Username: "SYS",
		Password: *test_pass,
		LogLevel: s.loglevel,
		Timeout:  10,
	})
	s.exaConn.DisableAutoCommit()
	defer s.exaConn.Disconnect()

	suite.Run(t, s)
}

func (s *testSuite) SetupTest() {
	var err error
	s.testDir, err = ioutil.TempDir(s.tmpDir, "exasol-test-data-")
	if err != nil {
		log.Fatal(err)
	}

	s.execute("DROP SCHEMA IF EXISTS [test] CASCADE")
	s.schemaSQL = "CREATE SCHEMA IF NOT EXISTS [test];\n"
	s.execute(s.schemaSQL)
}

func (s *testSuite) TearDownTest() {
	err := os.RemoveAll(s.testDir)
	if err != nil {
		fmt.Printf("Unable to remove test dir %s: %s", s.testDir, err)
	}
	s.exaConn.Rollback()
}

func (s *testSuite) execute(args ...string) {
	for _, arg := range args {
		_, err := s.exaConn.Execute(arg)
		if !s.exaConn.Conf.SuppressError {
			s.NoError(err, "Unable to execute SQL")
		}
	}
}

func (s *testSuite) backup(cnf Conf, args ...Object) {
	cnf.Source = s.exaConn
	cnf.Destination = s.testDir
	cnf.LogLevel = s.loglevel
	cnf.Objects = args
	Backup(cnf)
}

type dt map[string]interface{} // Directory/file Tree

func (s *testSuite) expect(expected dt) {
	s.expectDir(s.testDir, expected)
}

// This checks to see if the actual directory/file tree under 'dir'
// matches the expected directory/file tree specified by 'dt'
func (s *testSuite) expectDir(dir string, expected dt) {
	got, err := ioutil.ReadDir(dir)
	s.NoErrorf(err, "Unable to read dir %s: %s", dir, err)

	for _, fd := range got {
		name := fd.Name()
		fullPath := filepath.Join(dir, name)
		if fd.IsDir() {
			s.Containsf(expected, name, "Extra directory in backup %s", fullPath)
			expDir := expected[name]
			if expDir == nil {
				continue
			}
			s.expectDir(fullPath, expDir.(dt))
			delete(expected, name)

		} else {
			s.Containsf(expected, name, "Extra file in backup %s", fullPath)
			expContent := expected[name]
			if expContent == nil {
				continue
			}
			gotContent, err := ioutil.ReadFile(fullPath)
			s.NoErrorf(err, "Unable to read file %s: %s", fullPath, err)

			normalize := func(s string) string {
				s = regexp.MustCompile(`[[:blank]]`).ReplaceAllString(s, " ")
				s = regexp.MustCompile(`(?m)^\s+`).ReplaceAllString(s, "")
				s = regexp.MustCompile(`(?m)\s+$`).ReplaceAllString(s, "\n")
				return s
			}
			exp := normalize(expContent.(string))
			got := normalize(string(gotContent))

			s.Equal(exp, got)
			delete(expected, name)
		}
	}
	s.Emptyf(expected, "Missing backup entries under %s:\n%v", dir, expected)
}

func (s *testSuite) TestParameters() {
	s.backup(Conf{}, PARAMETERS)
	s.expect(dt{
		"parameters.sql": `
            ALTER SYSTEM SET CONSTRAINT_STATE_DEFAULT='ENABLE';
            ALTER SYSTEM SET DEFAULT_LIKE_ESCAPE_CHARACTER='\';
            ALTER SYSTEM SET DEFAULT_PRIORITY_GROUP='MEDIUM';
            ALTER SYSTEM SET NLS_DATE_FORMAT='YYYY-MM-DD';
            ALTER SYSTEM SET NLS_DATE_LANGUAGE='ENG';
            ALTER SYSTEM SET NLS_FIRST_DAY_OF_WEEK=7;
            ALTER SYSTEM SET NLS_NUMERIC_CHARACTERS='.,';
            ALTER SYSTEM SET NLS_TIMESTAMP_FORMAT='YYYY-MM-DD HH24:MI:SS.FF6';
            ALTER SYSTEM SET PASSWORD_EXPIRY_POLICY='OFF';
            ALTER SYSTEM SET PASSWORD_SECURITY_POLICY='OFF';
            ALTER SYSTEM SET PROFILE='OFF';
            ALTER SYSTEM SET QUERY_CACHE='ON';
            ALTER SYSTEM SET QUERY_TIMEOUT='0';
            ALTER SYSTEM SET SCRIPT_LANGUAGES='PYTHON=builtin_python R=builtin_r JAVA=builtin_java PYTHON3=builtin_python3';
            ALTER SYSTEM SET SCRIPT_OUTPUT_ADDRESS='';
            ALTER SYSTEM SET SQL_PREPROCESSOR_SCRIPT='';
            ALTER SYSTEM SET TIMESTAMP_ARITHMETIC_BEHAVIOR='INTERVAL';
            ALTER SYSTEM SET TIME_ZONE='EUROPE/BERLIN';
            ALTER SYSTEM SET TIME_ZONE_BEHAVIOR='INVALID SHIFT AMBIGUOUS ST';
        `,
	})
}

func (s *testSuite) TestSchemas() {
	adapterSQL := `
CREATE PYTHON ADAPTER SCRIPT [test].vs_adapter AS
import cjson
def adapter_call(js):
	req = cjson.decode(js)
	reqType = req['type']
	res = { 'type' : reqType }
	if reqType == 'createVirtualSchema':
		res['schemaMetadata'] = {
			'tables': [{
				'name': 'T',
				'columns': [{
					'name': 'C',
					'dataType': {'type': 'VARCHAR', 'size': 1}
				 }]
			}]
		}
	elif reqType == 'getCapabilities': res['capabilities'] = []
	elif reqType == 'pushdown': res['sql'] = 'SELECT 1'
	return cjson.encode(res).encode('utf-8')
`
	vSchemaSQL := `
		CREATE VIRTUAL SCHEMA IF NOT EXISTS [testvs]
	 	USING [test].[VS_ADAPTER]
		WITH
		  A = 'b'
		  P = 'v';
	`
	commentSQL := "COMMENT ON SCHEMA [test] IS 'HI MOM!!!';\n"
	sizeSQL := "ALTER SCHEMA [test] SET RAW_SIZE_LIMIT = 1234567890;\n"

	s.execute(adapterSQL, vSchemaSQL, commentSQL, sizeSQL)
	s.backup(Conf{}, SCHEMAS)
	s.expect(dt{
		"schemas": dt{
			"test": dt{
				"schema.sql": s.schemaSQL + commentSQL + sizeSQL,
			},
			"testvs": dt{
				"schema.sql": vSchemaSQL + "\n",
			},
		},
	})

	s.execute("DROP VIRTUAL SCHEMA IF EXISTS [testvs] CASCADE")
	s.execute("DROP ADAPTER SCRIPT [test].vs_adapter")
}

func (s *testSuite) TestTables() {
	table1SQL := `
		CREATE OR REPLACE TABLE "test"."T1" (
			"A" DECIMAL(18,0),
			"B" DECIMAL(18,0),
			PRIMARY KEY ("A","B")
		);
	`
	table2SQL := `
		CREATE OR REPLACE TABLE "test"."T2" (
			"A" DECIMAL(18,0) IDENTITY 321 NOT NULL COMMENT IS 'column A comment',
			"B" DECIMAL(18,0) COMMENT IS 'column B comment',
			"C" DECIMAL(18,0) DEFAULT 123 CONSTRAINT "cnst" NOT NULL DISABLE,
			FOREIGN KEY ("B","C") REFERENCES "test"."T1" ("A","B") DISABLE,
			CONSTRAINT "mypk" PRIMARY KEY ("A","C"),
			DISTRIBUTE BY "A","B",
			PARTITION BY "B","C"
		) COMMENT IS 'table comment';
	`
	data1SQL := `INSERT INTO [test].T1 VALUES (2,3), (3,4);`
	data2SQL := `INSERT INTO [test].T2 VALUES (1,2,3), (2,3,4);`
	s.execute(table1SQL, table2SQL, data1SQL, data2SQL)
	s.backup(Conf{MaxTableRows: 0}, TABLES)
	s.expect(dt{
		"schemas": dt{
			"test": dt{
				"tables": dt{
					"T1.sql": table1SQL,
					"T2.sql": table2SQL,
				},
			},
		},
	})

	// Test --max-table-rows
	s.backup(Conf{MaxTableRows: 100}, TABLES)
	s.expect(dt{
		"schemas": dt{
			"test": dt{
				"tables": dt{
					"T1.sql": table1SQL,
					"T2.sql": table2SQL,
					"T1.csv": "2,3\n3,4\n",
					"T2.csv": "1,2,3\n2,3,4\n",
				},
			},
		},
	})

	// Test --drop-extras
	s.execute("DROP TABLE t2")
	s.backup(Conf{DropExtras: true}, TABLES)
	s.expect(dt{
		"schemas": dt{
			"test": dt{
				"tables": dt{
					"T1.sql": table1SQL,
					"T1.csv": "2,3\n3,4\n",
				},
			},
		},
	})
}

func (s *testSuite) TestViews() {
	openSchemaSQL := "OPEN SCHEMA [test];\n"
	view1SQL := `CREATE OR REPLACE FORCE VIEW "test"."V1"
		  (c COMMENT IS 'column comment') AS
			SELECT 'Hi Mom!!' AS col
		  COMMENT IS 'view comment'`
	view2SQL := `CREATE OR REPLACE FORCE VIEW "test"."V2" AS SELECT 1 c`
	s.execute(openSchemaSQL, view1SQL, view2SQL)
	s.backup(Conf{MaxViewRows: 0}, VIEWS)
	s.expect(dt{
		"schemas": dt{
			"test": dt{
				"views": dt{
					"V1.sql": openSchemaSQL + view1SQL + ";\n",
					"V2.sql": openSchemaSQL + view2SQL + ";\n",
				},
			},
		},
	})

	// Test --max-view-rows
	s.backup(Conf{MaxViewRows: 100}, VIEWS)
	s.expect(dt{
		"schemas": dt{
			"test": dt{
				"views": dt{
					"V1.sql": openSchemaSQL + view1SQL + ";\n",
					"V1.csv": "\"Hi Mom!!\"\n",
					"V2.sql": openSchemaSQL + view2SQL + ";\n",
					"V2.csv": "1\n",
				},
			},
		},
	})

	// Test --drop-extras
	s.execute("DROP VIEW v2")
	s.backup(Conf{DropExtras: true}, VIEWS)
	s.expect(dt{
		"schemas": dt{
			"test": dt{
				"views": dt{
					"V1.sql": openSchemaSQL + view1SQL + ";\n",
					"V1.csv": "\"Hi Mom!!\"\n",
				},
			},
		},
	})

	// Test renamed views
	s.execute("RENAME VIEW v1 TO v3")
	view3SQL := regexp.MustCompile("V1").ReplaceAllString(view1SQL, "V3")
	s.backup(Conf{DropExtras: true}, VIEWS)
	s.expect(dt{
		"schemas": dt{
			"test": dt{
				"views": dt{
					"V3.sql": openSchemaSQL + view3SQL + ";\n",
				},
			},
		},
	})
}

func (s *testSuite) TestFunctions() {
	openSchemaSQL := "OPEN SCHEMA [test];"
	func1SQL := `--/
	    CREATE OR REPLACE FUNCTION "test"."F1" ()
		RETURN DECIMAL res DECIMAL; BEGIN RETURN 1; END;
		/
	`
	func2SQL := `CREATE OR REPLACE FUNCTION "test"."F2" ()
		RETURN DECIMAL res DECIMAL; BEGIN RETURN 1; END
	`
	commentSQL := "COMMENT ON FUNCTION [test].[F1] IS 'func comment';\n"
	s.execute(openSchemaSQL, func1SQL, func2SQL, commentSQL)
	s.backup(Conf{}, FUNCTIONS)
	s.expect(dt{
		"schemas": dt{
			"test": dt{
				"functions": dt{
					"F1.sql": openSchemaSQL + "\n" + func1SQL + commentSQL,
					"F2.sql": openSchemaSQL + "\n--/\n" + func2SQL + "\n/\n",
				},
			},
		},
	})

	// Test --drop-extras
	s.execute("DROP FUNCTION f2")
	s.backup(Conf{DropExtras: true}, FUNCTIONS)
	s.expect(dt{
		"schemas": dt{
			"test": dt{
				"functions": dt{
					"F1.sql": openSchemaSQL + "\n" + func1SQL + commentSQL,
				},
			},
		},
	})
}

func (s *testSuite) TestScripts() {
	openSchemaSQL := "OPEN SCHEMA [test];"
	script1SQL := `--/
		CREATE OR REPLACE LUA SCRIPT "SCRIPTING_SCRIPT" () RETURNS ROWCOUNT AS
		function hi()
			output('hello')
		end
/
`
	script2SQL := `CREATE OR REPLACE LUA SCALAR SCRIPT "UDF_SCRIPT" () RETURNS DECIMAL(18,0) AS
		function run(ctx)
			return 1
		end
	`
	script3SQL := `CREATE OR REPLACE PYTHON  ADAPTER SCRIPT "ADAPTER_SCRIPT" AS
		local str = 'hello'
	`
	commentSQL := "COMMENT ON SCRIPT [test].[SCRIPTING_SCRIPT] IS 'script comment';\n"
	s.execute(openSchemaSQL, script1SQL, script2SQL, script3SQL, commentSQL)
	s.backup(Conf{}, SCRIPTS)
	s.expect(dt{
		"schemas": dt{
			"test": dt{
				"scripts": dt{
					"SCRIPTING_SCRIPT.sql": fmt.Sprintf("%s\n%s%s",
						openSchemaSQL, script1SQL, commentSQL,
					),
					"UDF_SCRIPT.sql":     openSchemaSQL + "\n--/\n" + script2SQL + "\n/\n",
					"ADAPTER_SCRIPT.sql": openSchemaSQL + "\n--/\n" + script3SQL + "\n/\n",
				},
			},
		},
	})

	// Test --drop-extras
	s.execute("DROP SCRIPT SCRIPTING_SCRIPT")
	s.execute("DROP ADAPTER SCRIPT ADAPTER_SCRIPT")
	s.backup(Conf{DropExtras: true}, SCRIPTS)
	s.expect(dt{
		"schemas": dt{
			"test": dt{
				"scripts": dt{
					"UDF_SCRIPT.sql": openSchemaSQL + "\n--/\n" + script2SQL + "\n/\n",
				},
			},
		},
	})
}

func (s *testSuite) TestUsers() {
	password := regexp.MustCompile(`"12345678"`)
	user1SQL := "CREATE USER JOE IDENTIFIED BY \"12345678\";\n"
	user2SQL := "CREATE USER JANE IDENTIFIED BY KERBEROS PRINCIPAL 'jane';\n"
	user3SQL := "CREATE USER JOHN IDENTIFIED AT LDAP AS 'john'"
	commentSQL := "COMMENT ON USER JOE IS 'a tough guy';\n"
	policySQL := "ALTER USER JOE SET PASSWORD_EXPIRY_POLICY='EXPIRY_DAYS=180:GRACE_DAYS=30';\n"
	expireSQL := "ALTER USER JOE PASSWORD EXPIRE;\n"
	cleanUser1SQL := password.ReplaceAllString(user1SQL, "********")

	s.execute("DROP USER IF EXISTS joe")
	s.execute("DROP USER IF EXISTS jane")
	s.execute("DROP USER IF EXISTS john")
	s.execute(user1SQL, user2SQL, user3SQL+" FORCE", commentSQL, policySQL, expireSQL)
	s.backup(Conf{}, USERS)
	s.expect(dt{
		"users": dt{
			"JOE.sql":  cleanUser1SQL + commentSQL + policySQL + expireSQL,
			"JANE.sql": user2SQL,
			"JOHN.sql": user3SQL + ";\n",
		},
	})
}

func (s *testSuite) TestRoles() {
	roleSQL := "CREATE ROLE LUMBERJACKS;\n"
	commentSQL := "COMMENT ON ROLE LUMBERJACKS IS 'tough guys';\n"

	s.execute("DROP ROLE IF EXISTS lumberjacks")
	s.execute(roleSQL, commentSQL)
	s.backup(Conf{}, ROLES)
	s.expect(dt{
		"roles": dt{
			"DBA.sql":         "COMMENT ON ROLE DBA IS 'DBA stands for database administrator and has all possible privileges. This role should only be assigned to very few users because it provides these with full access to the database.';\n",
			"PUBLIC.sql":      "COMMENT ON ROLE PUBLIC IS 'The PUBLIC role stands apart because every user receives this role automatically. This makes it very simple to grant and later withdraw certain privileges to/from all users of the database. However, this should only occur if one is quite sure that it is safe to grant the respective rights and the shared data should be publicly accessible.';\n",
			"LUMBERJACKS.sql": roleSQL + commentSQL,
		},
	})
}

func (s *testSuite) TestConnections() {
	password := regexp.MustCompile(`'12345678'`)
	connSQL := "CREATE OR REPLACE CONNECTION CONN TO 'someplace' USER 'joe' IDENTIFIED BY '12345678';\n"
	commentSQL := "COMMENT ON CONNECTION CONN IS 'teleporter';\n"
	cleanConnSQL := password.ReplaceAllString(connSQL, "********")

	s.execute("DROP CONNECTION IF EXISTS conn")
	s.execute(connSQL, commentSQL)
	s.backup(Conf{}, CONNECTIONS)
	s.expect(dt{
		"connections.sql": cleanConnSQL + commentSQL,
	})
}

func (s *testSuite) TestPriorityGroups() {
	groupSQL := []string{
		"DROP PRIORITY GROUP [Low]",
		"CREATE PRIORITY GROUP [Low] WITH WEIGHT = 234",
		"ALTER PRIORITY GROUP [MEDIUM] SET WEIGHT = 345",
		"DROP PRIORITY GROUP [custom]",
		"CREATE PRIORITY GROUP [custom] WITH WEIGHT = 456",
		"COMMENT ON PRIORITY GROUP [custom] IS 'the big cheeses'",
		"DROP PRIORITY GROUP [high]",
		"CREATE PRIORITY GROUP [high] WITH WEIGHT = 123",
	}
	s.exaConn.Conf.SuppressError = true // The groups may not exist
	s.execute("DROP PRIORITY GROUP HIGH")
	s.execute("DROP PRIORITY GROUP LOW")
	s.execute(groupSQL...)
	s.exaConn.Conf.SuppressError = false
	s.backup(Conf{}, PRIORITY_GROUPS)
	s.expect(dt{"priority_groups.sql": strings.Join(groupSQL, ";\n") + ";\n"})
	s.execute("DROP PRIORITY GROUP [custom]")
}

func (s *testSuite) TestPrivileges() {
	sql := []string{
		"CREATE USER JOE IDENTIFIED BY KERBEROS PRINCIPAL 'joe'",
		"GRANT PRIORITY GROUP [LOW] TO JOE",                          // Priority Priv
		"GRANT CONNECTION CONN TO JOE WITH ADMIN OPTION",             // Connection Priv
		"GRANT SELECT ON SCHEMA [test] TO JOE",                       // Object Priv
		"GRANT ACCESS ON CONNECTION [CONN] FOR SCHEMA [test] TO JOE", // Connection Restricted Priv
		"GRANT DBA TO JOE WITH ADMIN OPTION",                         // Role Priv
		"GRANT SELECT ANY TABLE TO JOE WITH ADMIN OPTION",            // System Priv
		"GRANT IMPERSONATION ON DBA TO JOE",                          // Impersonation Priv
		"ALTER SCHEMA [test] CHANGE OWNER JOE",                       // Schema Owner
	}
	s.execute("DROP USER IF EXISTS joe")
	s.execute("DROP CONNECTION IF EXISTS conn")
	s.execute("CREATE CONNECTION conn TO 'someplace'")
	s.execute(sql...)
	s.backup(Conf{}, USERS)
	s.expect(dt{
		"users": dt{
			"JOE.sql": strings.Join(sql, ";\n") + ";\n",
		},
	})
}
