#!/bin/sh
# Test double for the document engine CLI. It simulates outputs and errors; it
# never touches the network. Behaviour switches come from file contents:
#   source/ops containing SLEEP    -> sleeps 30 seconds
#   source/ops containing CRASH    -> exit 9 without output
#   source/ops containing GARBAGE  -> prints text that is not JSON
#   source/ops containing ENVDUMP  -> the produced file carries `env`
#   ops containing FAILED1|SKIPPED1|NOBATCH -> docs apply reports failed, skipped or no batch (exit 0)
#   input containing EMPTYANSWER   -> read commands print `{}` (exit 0)
#   ops containing PARTIAL         -> docs apply reports total 1, applied 1 whatever the batch size
#   ops containing FORMATONLY      -> with --track, a document with only an rPrChange
#   ops containing NOREVISIONS     -> with --track, a plain document (no revisions)
#   ops containing CELLSOFF|NOCELLS -> sheet apply --cells reports one cell too many, or none
#   before.docx containing INSZERO|DELZERO|INSONLY -> compare announces counts that contradict the revisions in the file
#   source/ops containing BADXML   -> a ZIP whose two required parts hold the text "invalide"
#   source/ops containing INVALID  -> the produced file is not a package, JSON is `{}`, exit 0
#   ops containing REJECT          -> an atomic rejection (exit 1, detail.failures)
#   input named MISSING            -> exit 2 outside_allowed_roots-style failure
set -u
[ "${1:-}" = "--version" ] && { echo "genoffice 0.10.0"; exit 0; }

out=""; ops=""; src=""; from=""; author=""; type=""; track=0
args="$*"
while [ $# -gt 0 ]; do
  case "$1" in
    --out) out="$2"; shift ;;
    --ops|--cells) ops="$2"; shift ;;
    --from) from="$2"; shift ;;
    --author) author="$2"; shift ;;
    --type) type="$2"; shift ;;
    --track) track=1 ;;
  esac
  shift
done
cmd="$(echo "$args" | cut -d' ' -f1)"
case "$cmd" in info|convert|docs|sheet) if cat input.docx input.xlsx 2>/dev/null | grep -q EMPTYANSWER && [ "$(echo "$args" | cut -d' ' -f2)" != apply ] && [ "$(echo "$args" | cut -d' ' -f2)" != compare ]; then echo '{}'; exit 0; fi ;; esac
sub="$(echo "$args" | cut -d' ' -f2)"

probe=""
for f in "$from" "$ops"; do [ -n "$f" ] && [ -f "$f" ] && probe="$probe $(cat "$f")"; done
case "$probe" in *SLEEP*) sleep 30 ;; esac
case "$probe" in *CRASH*) exit 9 ;; esac
case "$probe" in *INVALID*) printf 'document invalide' > "$out"; echo "{}"; exit 0 ;; esac
case "$probe" in *GARBAGE*) echo "this is not json"; exit 0 ;; esac
case "$probe" in *REJECT*)
  printf '{"status":"error","command":"docs","code":1,"error":"target_not_found","message":"op 0 (findReplace) rejected: No matching blocks; the document was not changed. See `genoffice guide docs`.","suggestion":"run genoffice guide docs","detail":{"failures":[{"index":0,"op":"findReplace","error":"No matching blocks in %s","reason":"target_not_found"}],"batch":{"total":1,"applied":0,"failed":1,"skipped":0}}}\n' "$PWD"
  exit 1 ;;
esac

# A valid minimal package (testdata/minimal.docx|xlsx); the marker and ENVDUMP are
# written into its 8 KiB zip comment so the file stays a readable archive.
here=$(cd "$(dirname "$0")" && pwd)
write_pkg() {
  case "$out" in *.xlsx) tpl="$here/minimal.xlsx" ;; *) tpl="$here/minimal.docx" ;; esac
  [ -n "${TPL:-}" ] && tpl="$here/$TPL"
  case "$probe" in *BADXML*) tpl="$here/badxml.docx" ;; esac
  cp "$tpl" "$out"
  size=$(wc -c < "$out" | tr -d ' ')
  {
    printf 'fake-%s' "$1"
    case "$probe" in *ENVDUMP*) echo; echo "PWD=$PWD"; env | sort ;; esac
  } | head -c 8192 > "$out.comment"
  dd if="$out.comment" of="$out" bs=1 seek=$((size - 8192)) conv=notrunc 2>/dev/null
  rm -f "$out.comment"
}

inp=$(cat input.docx input.xlsx 2>/dev/null)
ok() { printf '{"status":"ok","command":"x","summary":"x","detail":%s}\n' "$1"; exit 0; }
# BAD<X>: a mistyped answer; INC<X>: an incomplete or inconsistent one (exit 0)
case "$inp$probe:$cmd $sub" in
  *BADCREATE*:"create "*) write_pkg x; ok '{"sheets":"invalide"}' ;;
  *INCCREATE*:"create "*) ok '{}' ;;
  *BADAPPLY*:"docs apply") write_pkg x; ok '{"batch":{"total":1,"applied":"one","failed":0,"skipped":0}}' ;;
  *INCAPPLY*:"docs apply") write_pkg x; ok '{"batch":{"total":2,"applied":1,"failed":0,"skipped":0}}' ;;
  *BADAPPLY*:"sheet apply") write_pkg x; ok '{"cells":"three"}' ;;
  *INCAPPLY*:"sheet apply") write_pkg x; ok '{"cells":0}' ;;
  *BADINFO*:"info input.docx") ok '{"blocks":"four"}' ;;
  *INCINFO*:"info input.docx") ok '{"headings":[]}' ;;
  *BADINFO*:"info input.xlsx") ok '{"sheets":"invalide"}' ;;
  *INCINFO*:"info input.xlsx") ok '{"sheets":[{"rows":1}]}' ;;
  *BADCHECK*:"docs check"|*BADCHECK*:"sheet check") ok '{"issues":"none"}' ;;
  *INCCHECK*:"docs check"|*INCCHECK*:"sheet check") ok '{"counts":{}}' ;;
  *BADREAD*:"sheet read") ok '{"rows":"x"}' ;;
  *INCREAD*:"sheet read") ok '{"range":"A1"}' ;;
esac
case "$cmd $sub" in
  "create "*)
    write_pkg "create-$type"
    case "$type" in
      xlsx) printf '{"status":"ok","command":"create","summary":"created","output_path":"%s","detail":{"sheets":1,"cells":6,"formulas":0}}\n' "$PWD/$out" ;;
      *) printf '{"status":"ok","command":"create","summary":"created","output_path":"%s","detail":{"source":"markdown","blocks":3}}\n' "$PWD/$out" ;;
    esac ;;
  "docs apply")
    grep -q MISSING input.docx 2>/dev/null && { echo '{"status":"error","command":"docs","code":2,"error":"file_error","message":"cannot read the file"}'; exit 2; }
    if [ "$track" = 1 ]; then case "$probe" in *NOREVISIONS*) ;; *FORMATONLY*) TPL=formatonly.docx ;; *) TPL=tracked.docx ;; esac; fi
    write_pkg "docs-apply-track$track-$author"
    n=$(grep -o '"op"' "$ops" | wc -l | tr -d ' ')
    case "$probe" in
      *FAILED1*) printf '{"status":"ok","command":"docs","summary":"applied","output_path":"%s","detail":{"batch":{"total":%s,"applied":0,"failed":1,"skipped":0}}}\n' "$PWD/$out" "$n"; exit 0 ;;
      *SKIPPED1*) printf '{"status":"ok","command":"docs","summary":"applied","output_path":"%s","detail":{"batch":{"total":%s,"applied":0,"failed":0,"skipped":1}}}\n' "$PWD/$out" "$n"; exit 0 ;;
      *NOBATCH*) printf '{"status":"ok","command":"docs","summary":"applied","output_path":"%s","detail":{}}\n' "$PWD/$out"; exit 0 ;;
    esac
    case "$probe" in *PARTIAL*) printf '{"status":"ok","command":"docs","summary":"applied","output_path":"%s","detail":{"batch":{"total":1,"applied":1,"failed":0,"skipped":0}}}\n' "$PWD/$out"; exit 0 ;; esac
    printf '{"status":"ok","command":"docs","summary":"applied","output_path":"%s","detail":{"batch":{"total":%s,"applied":%s,"failed":0,"skipped":0}}}\n' "$PWD/$out" "$n" "$n" ;;
  "sheet apply")
    write_pkg "sheet-apply"
    case "$args" in
      *--ops*) case "$probe" in *NOBATCH*) printf '{"status":"ok","command":"sheet","summary":"applied","output_path":"%s","detail":{}}\n' "$PWD/$out"; exit 0 ;; esac
        printf '{"status":"ok","command":"sheet","summary":"applied","output_path":"%s","detail":{"batch":{"total":1,"applied":1,"failed":0,"skipped":0}}}\n' "$PWD/$out" ;;
      *) n=$(grep -o '"cell"' "$ops" | wc -l | tr -d ' ')
         case "$probe" in *CELLSOFF*) n=$((n + 1)) ;; *NOCELLS*) printf '{"status":"ok","command":"sheet","summary":"wrote","output_path":"%s","detail":{}}\n' "$PWD/$out"; exit 0 ;; esac
         printf '{"status":"ok","command":"sheet","summary":"wrote","output_path":"%s","detail":{"cells":%s,"formulas":1}}\n' "$PWD/$out" "$n" ;;
    esac ;;
  "docs compare")
    inp=$(cat before.docx after.docx 2>/dev/null)
    case "$inp" in
      *INVALID*) printf 'document invalide' > "$out"; echo '{}'; exit 0 ;;
      *SAMEDOC*) write_pkg "compare-$author"
        printf '{"status":"ok","command":"docs","summary":"same","output_path":"%s","detail":{"insertions":0,"deletions":0,"changed_paragraphs":0}}\n' "$PWD/$out"; exit 0 ;;
      *NOREVS*) write_pkg "compare-$author"
        printf '{"status":"ok","command":"docs","summary":"x","output_path":"%s","detail":{"insertions":2,"deletions":3}}\n' "$PWD/$out"; exit 0 ;;
      *INSZERO*) TPL=tracked.docx write_pkg "compare-$author"
        printf '{"status":"ok","command":"docs","summary":"x","output_path":"%s","detail":{"insertions":0,"deletions":3}}\n' "$PWD/$out"; exit 0 ;;
      *DELZERO*) TPL=tracked.docx write_pkg "compare-$author"
        printf '{"status":"ok","command":"docs","summary":"x","output_path":"%s","detail":{"insertions":2,"deletions":0}}\n' "$PWD/$out"; exit 0 ;;
      *INSONLY*) TPL=insonly.docx write_pkg "compare-$author"
        printf '{"status":"ok","command":"docs","summary":"x","output_path":"%s","detail":{"insertions":2,"deletions":3}}\n' "$PWD/$out"; exit 0 ;;
      *BADTYPE*) TPL=tracked.docx write_pkg "compare-$author"
        printf '{"status":"ok","command":"docs","summary":"x","output_path":"%s","detail":{"insertions":"two","deletions":3}}\n' "$PWD/$out"; exit 0 ;;
      *INCOMPLETE*) TPL=tracked.docx write_pkg "compare-$author"
        printf '{"status":"ok","command":"docs","summary":"x","output_path":"%s","detail":{"changed_paragraphs":2}}\n' "$PWD/$out"; exit 0 ;;
    esac
    TPL=tracked.docx write_pkg "compare-$author"
    printf '{"status":"ok","command":"docs","summary":"compared","output_path":"%s","detail":{"insertions":2,"deletions":3,"changed_paragraphs":2,"author":"%s"}}\n' "$PWD/$out" "$author" ;;
  "info "*)
    case "$args" in
      *input.xlsx*) printf '{"status":"ok","command":"info","summary":"xlsx","detail":{"format":"xlsx","sheets":[{"name":"Devis","rows":3,"columns":2},{"name":"Notes","rows":1,"columns":1}],"defined_names":0}}\n' ;;
      *) printf '{"status":"ok","command":"info","summary":"docx","detail":{"format":"docx","blocks":4,"block_types":{"heading":1,"paragraph":2,"table":1},"headings":[{"level":1,"text":"Title"}],"comments":0,"footnotes":0,"endnotes":0,"protected":false,"properties":{"app":{"application":"GenOffice"}}}}\n' ;;
    esac ;;
  "convert "*)
    printf '# Title\n\nHello world\n\n| a | b |\n|---|---|\n| 1 | 2 |\n' > "$out"
    printf '{"status":"ok","command":"convert","summary":"converted","output_path":"%s","detail":{"blocks":4}}\n' "$PWD/$out" ;;
  "docs check")
    printf '{"status":"ok","command":"docs","summary":"1 issue","detail":{"issues":[{"check":"placeholder_left","severity":"warning","message":"placeholder text left","block":2}],"counts":{"error":0,"warning":1,"info":0}}}\n' ;;
  "sheet read")
    case "$args" in
      *Notes*) printf '{"status":"ok","command":"sheet","summary":"read","detail":{"rows":[["note"]],"formulas":{},"truncated":false,"range":"A1:A1"}}\n' ;;
      *) printf '{"status":"ok","command":"sheet","summary":"read","detail":{"rows":[["Ref","Total"],["L01",900],[null,"=SUM"]],"formulas":{"B3":"=SUM(B2:B2)"},"truncated":false,"range":"A1:B3"}}\n' ;;
    esac ;;
  "sheet check")
    printf '{"status":"ok","command":"sheet","summary":"no issues","detail":{"issues":[],"counts":{"error":0,"warning":0,"info":0}}}\n' ;;
  *)
    printf '{"status":"error","command":"%s","code":1,"error":"unsupported","message":"unknown command"}\n' "$cmd"; exit 1 ;;
esac
