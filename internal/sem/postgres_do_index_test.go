package sem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresDoLanguagePreservesDeclarations(t *testing.T) {
	for _, wrapper := range [][2]string{
		{"DO $body$", "$body$ LANGUAGE 'plpgsql';"},
		{"DO LANGUAGE plpgsql $body$", "$body$;"},
		{"DO LANGUAGE 'plpgsql' $$", "$$ ;"},
		{"DO $Case_Sensitive$", "$Case_Sensitive$ LANGUAGE plpgsql;"},
		{"DO $$", "$$;"},
	} {
		t.Run(wrapper[0]+wrapper[1], func(t *testing.T) {
			src := "CREATE TABLE before_tbl (id int);\n" + wrapper[0] + "\nBEGIN\n" +
				"  IF true THEN\n" +
				"    CREATE TABLE nested_tbl (unique_key int, CONSTRAINT positive CHECK (unique_key > 0));\n" +
				"    CREATE INDEX nested_idx ON nested_tbl (unique_key) WHERE unique_key IS NOT NULL;\n" +
				"  END IF;\n" +
				"  EXECUTE $other$CREATE TABLE phantom (id int);$other$;\n" +
				"  -- CREATE TABLE commented (id int);\n" +
				"END;\n" + wrapper[1] + "\nCREATE TABLE after_tbl (id int);\n"
			entities, _, status := (TreeSitterParser{}).ParseWithStatus("migration.sql", src)
			if status.ParseError {
				t.Fatalf("parse error: %s\n%s", status.Detail, maskPostgresUnsupportedSyntax(src))
			}
			for _, name := range []string{"before_tbl", "nested_tbl", "after_tbl"} {
				if countEntity(entities, "table", name) != 1 {
					t.Errorf("missing %s: %+v", name, entities)
				}
			}
			if countEntity(entities, "index", "nested_idx") != 1 {
				t.Errorf("missing index: %+v", entities)
			}
			for _, name := range []string{"phantom", "commented"} {
				if countEntity(entities, "table", name) != 0 {
					t.Errorf("phantom %s: %+v", name, entities)
				}
			}
			for _, e := range entities {
				if e.Name == "nested_tbl" && (e.StartLine != 5 || e.EndLine != 5) {
					t.Errorf("nested declaration moved: %+v", e)
				}
				if e.Name == "after_tbl" && e.StartLine != 12 {
					t.Errorf("following declaration moved: %+v", e)
				}
			}
			masked := maskPostgresUnsupportedSyntax(src)
			if len(masked) != len(src) {
				t.Fatal("changed source width")
			}
			for i := range src {
				if (src[i] == '\n' || src[i] == '\r') && src[i] != masked[i] {
					t.Fatal("moved newline")
				}
			}
		})
	}
}

func TestPostgresConstraintKeywordPrefixes(t *testing.T) {
	for _, name := range []string{"unique_key", "unique_states", "constraint_name", "unique2"} {
		t.Run(name, func(t *testing.T) {
			src := "CREATE TABLE jobs (" + name + " int, id int);\n" +
				"CREATE UNIQUE INDEX jobs_idx ON jobs (id, " + name + ") WHERE " + name + " IS NOT NULL;\n" +
				"CREATE INDEX jobs_single ON jobs (" + name + ");\n"
			if got := maskPostgresUnsupportedSyntax(src); got != src {
				t.Fatalf("identifier changed:\n%s", got)
			}
			entities, _, status := (TreeSitterParser{}).ParseWithStatus("indexes.sql", src)
			if status.ParseError {
				t.Fatalf("parse error: %s", status.Detail)
			}
			if countEntity(entities, "table", "jobs") != 1 || countEntity(entities, "index", "jobs_idx") != 1 || countEntity(entities, "index", "jobs_single") != 1 {
				t.Fatalf("declarations missing: %+v", entities)
			}
		})
	}
}

// These are identifier continuations too, even though the bundled grammar
// does not yet accept every unquoted PostgreSQL identifier spelling.
func TestPostgresConstraintKeywordNonASCIIAndDollarBoundaries(t *testing.T) {
	for _, name := range []string{"unique$value", "uniqueé", "constraint$name", "constrainté"} {
		src := "CREATE TABLE jobs (" + name + " int);"
		if got := maskPostgresUnsupportedSyntax(src); got != src {
			t.Errorf("identifier changed: %s", got)
		}
	}
}

func TestPostgresDoMalformedWrapperStillFails(t *testing.T) {
	for _, src := range []string{
		"DO $body$ BEGIN NULL; END; $other$ LANGUAGE plpgsql;",
		"DO $body$ BEGIN NULL; END; $BODY$;",
		"DO $$ BEGIN NULL; END; $$ LANGUAGE;",
		"DO LANGUAGE plpgsql $$ BEGIN NULL; END; $$ LANGUAGE plpgsql;",
		"DO $$ BEGIN NULL; END; $$ trailing_garbage;",
		"DO $$ BEGIN CREATE TABLE broken (id int; END; $$ LANGUAGE plpgsql;",
	} {
		_, _, status := (TreeSitterParser{}).ParseWithStatus("bad.sql", src)
		if !status.ParseError {
			t.Errorf("malformed source accepted: %s", src)
		}
	}
}

func TestPostgresDoTextInsideLiteralIsUntouched(t *testing.T) {
	for _, value := range []string{`'DO $$ BEGIN NULL; END; $$ LANGUAGE plpgsql;'`, `$outer$DO $$ BEGIN NULL; END; $$;$outer$`, `"DO $$ BEGIN NULL; END; $$;"`} {
		src := "CREATE TABLE t (label text DEFAULT " + value + ");"
		if got := maskPostgresUnsupportedSyntax(src); got != src {
			t.Errorf("literal changed: %s", got)
		}
	}
	// The two blocks have different tags, including a quoted dollar tag in a body.
	src := "DO $first$ BEGIN RAISE NOTICE '$second$;'; END; $first$ LANGUAGE plpgsql;\nDO $second$ BEGIN NULL; END; $second$;\nCREATE TABLE tail_tbl (id int);\n"
	entities, _, status := (TreeSitterParser{}).ParseWithStatus("blocks.sql", src)
	if status.ParseError || countEntity(entities, "table", "tail_tbl") != 1 {
		t.Fatalf("adjacent blocks: %+v %+v", status, entities)
	}
	if strings.Count(maskPostgresUnsupportedSyntax(src), "\n") != strings.Count(src, "\n") {
		t.Fatal("changed line count")
	}
}

// Pinned, offline River fixtures cover the four previously failing migrations
// and ensure fixing them never discards declarations inside anonymous blocks.
func TestPostgresRiverMigrationCorpus(t *testing.T) {
	repo := t.TempDir()
	paths, err := filepath.Glob("testdata/postgres-river/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 16 {
		t.Fatalf("migration count = %d, want 16", len(paths))
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, repo, filepath.Base(path), string(data))
	}
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Header.PartialFailures) != 0 {
		t.Fatalf("River failures: %+v", snapshot.Header.PartialFailures)
	}
	if snapshot.Header.Stats.CompletenessLevel != "ok" {
		t.Fatalf("incomplete River snapshot: %+v", snapshot.Header.Stats)
	}
	want := map[string]bool{
		"001_create_river_migration.up.sql:index:river_migration:8-8":                                               true,
		"001_create_river_migration.up.sql:table:river_migration:1-6":                                               true,
		"002_initial_schema.up.sql:function:river_job_notify:62-78":                                                 true,
		"002_initial_schema.up.sql:index:river_job_args_index:58-58":                                                true,
		"002_initial_schema.up.sql:index:river_job_kind:52-52":                                                      true,
		"002_initial_schema.up.sql:index:river_job_metadata_index:60-60":                                            true,
		"002_initial_schema.up.sql:index:river_job_prioritized_fetching_index:56-56":                                true,
		"002_initial_schema.up.sql:index:river_job_state_and_finalized_at_index:54-54":                              true,
		"002_initial_schema.up.sql:table:river_job:11-48":                                                           true,
		"002_initial_schema.up.sql:table:river_leader:85-96":                                                        true,
		"002_initial_schema.up.sql:trigger:river_notify:80-83":                                                      true,
		"004_pending_and_more.down.sql:function:river_job_notify:14-30":                                             true,
		"004_pending_and_more.down.sql:trigger:river_notify:32-35":                                                  true,
		"004_pending_and_more.up.sql:table:river_queue:30-36":                                                       true,
		"005_migration_unique_client.down.sql:index:river_migration:32-32":                                          true,
		"005_migration_unique_client.down.sql:table:river_migration:25-30":                                          true,
		"005_migration_unique_client.up.sql:index:river_job_kind_unique_key_idx:45-45":                              true,
		"005_migration_unique_client.up.sql:table:river_client:55-62":                                               true,
		"005_migration_unique_client.up.sql:table:river_client_queue:66-79":                                         true,
		"005_migration_unique_client.up.sql:table:river_migration:15-22":                                            true,
		"006_bulk_unique.down.sql:index:river_job_kind_unique_key_idx:11-11":                                        true,
		"006_bulk_unique.up.sql:function:river_job_state_in_bitmask:1-17":                                           true,
		"006_bulk_unique.up.sql:index:river_job_unique_idx:30-33":                                                   true,
		"007_notification_outbox_sqlite_jsonb_and_sql_cleanup.down.sql:table:river_client:9-16":                     true,
		"007_notification_outbox_sqlite_jsonb_and_sql_cleanup.down.sql:table:river_client_queue:18-31":              true,
		"007_notification_outbox_sqlite_jsonb_and_sql_cleanup.up.sql:index:river_notification_created_at_idx:13-13": true,
		"007_notification_outbox_sqlite_jsonb_and_sql_cleanup.up.sql:index:river_notification_topic_id_idx:14-14":   true,
		"007_notification_outbox_sqlite_jsonb_and_sql_cleanup.up.sql:table:river_notification:5-11":                 true,
	}
	for _, symbol := range snapshot.Symbols {
		key := fmt.Sprintf("%s:%s:%s:%d-%d", symbol.FilePath, symbol.Kind, symbol.QualifiedName, symbol.StartLine, symbol.EndLine)
		if !want[key] {
			t.Errorf("unexpected or moved declaration: %s", key)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Errorf("missing declarations: %v", want)
	}
	if len(snapshot.Relations) != 33 {
		t.Errorf("relation count = %d, want 33", len(snapshot.Relations))
	}
}

func TestPostgresDoEscapedLiteralDoesNotHideDeclarations(t *testing.T) {
	src := `DO $body$
BEGIN
 RAISE NOTICE E'it\'s -- CREATE TABLE phantom (id int);';
 CREATE TABLE "it's_real" (id int);
 CREATE INDEX real_idx ON "it's_real" (id);
END;
$body$ LANGUAGE plpgsql;
`
	entities, _, status := (TreeSitterParser{}).ParseWithStatus("quoted.sql", src)
	if status.ParseError {
		t.Fatalf("parse error: %s", status.Detail)
	}
	if countEntity(entities, "table", "it's_real") != 1 || countEntity(entities, "index", "real_idx") != 1 || countEntity(entities, "table", "phantom") != 0 {
		t.Fatalf("quoted text hid or invented declarations: %+v", entities)
	}
}
