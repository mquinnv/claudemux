#!/bin/bash
# Shared project-color resolver. Source this, then call:
#     resolve_project_hex "$start_dir"
# It walks $start_dir and ancestors for the nearest project color, preferring
# .project.yml (`color: <named|#hex>`) then legacy .project-color (#hex/named),
# and echoes a 6-digit hex (no leading #). Returns 1 if nothing is found.
#
# Single source of truth for the named palette — used by the tmux chrome hooks
# and by claudemux. Tune the hexes here to taste.
#
# Named colors mirror Claude Code's /color set:
#   red blue green yellow purple orange pink cyan

# name_to_hex NAME|#HEX|HEX -> 6-digit hex (no #), or non-zero if unrecognized.
name_to_hex() {
    case "$1" in
        red)    echo ff4d4d ;;
        blue)   echo 4d79ff ;;
        green)  echo 4dd24d ;;
        yellow) echo ffd24d ;;
        purple) echo b34dff ;;
        orange) echo ff944d ;;
        pink)   echo ff4dd2 ;;
        cyan)   echo 4dd2d2 ;;
        \#*)
            if printf '%s' "${1#\#}" | grep -qiE '^[0-9a-fA-F]{6}$'; then
                printf '%s' "${1#\#}"
            else
                return 1
            fi
            ;;
        *)
            if printf '%s' "$1" | grep -qiE '^[0-9a-fA-F]{6}$'; then
                printf '%s' "$1"
            else
                return 1
            fi
            ;;
    esac
}

resolve_project_hex() {
    local dir="$1" raw=""
    while [ -n "$dir" ] && [ "$dir" != "/" ]; do
        if [ -f "$dir/.project.yml" ]; then
            raw=$(sed -n 's/^[[:space:]]*color:[[:space:]]*//p' "$dir/.project.yml" \
                  | head -1 | tr -d "\"' \r")
            [ -n "$raw" ] && break
        fi
        if [ -f "$dir/.project-color" ]; then
            raw=$(tr -d '[:space:]' < "$dir/.project-color")
            [ -n "$raw" ] && break
        fi
        dir=$(dirname "$dir")
    done
    [ -n "$raw" ] || return 1
    name_to_hex "$raw"
}

# resolve_project_style DIR -> "<hex> <fg>", e.g. "b34dff #ffffff".
#
# The hex is resolve_project_hex's; the foreground is the black/white pick that
# keeps text legible on it, by the same luminance rule the tmux status bar has
# always used. Both callers — claudemux at launch and claudemux-head at reset —
# come through here, so a change to the palette or the contrast rule lands in
# both at once. Returns 1 with no output when there is no project color.
resolve_project_style() {
    local hex r g b luminance
    hex="$(resolve_project_hex "$1")" || return 1
    [ -n "$hex" ] || return 1
    r=$((16#${hex:0:2})); g=$((16#${hex:2:2})); b=$((16#${hex:4:2}))
    luminance=$(( (r * 299 + g * 587 + b * 114) / 1000 ))
    if [ "$luminance" -gt 150 ]; then
        echo "$hex #000000"
    else
        echo "$hex #ffffff"
    fi
}
