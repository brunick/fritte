#!/usr/bin/env python3
"""
Extrahiert Inhalte aus einer HAR-Datei in eine Verzeichnisstruktur.

Das Script sucht im gleichen Verzeichnis nach einer *.har-Datei,
liest alle Eintraege aus und speichert die HTTP-Antworten unter
extracted/<host>/<pfad>/<datei>.<endung> ab.

Base64-kodierte Inhalte werden automatisch decodiert.
"""

import base64
import json
import mimetypes
import os
import re
import sys
import urllib.parse
from pathlib import Path


def find_har_files(directory: Path) -> list[Path]:
    """Sucht alle HAR-Dateien im angegebenen Verzeichnis."""
    candidates = sorted(directory.glob("*.har"))
    if not candidates:
        raise FileNotFoundError(f"Keine HAR-Datei in {directory} gefunden.")
    return candidates


def guess_extension(mime_type: str, url_path: str) -> str:
    """Bestimmt eine passende Dateiendung aus MIME-Typ oder URL-Pfad."""
    if mime_type:
        ext = mimetypes.guess_extension(mime_type.split(";")[0].strip())
        if ext:
            return ext

    known_extensions = {
        "application/javascript": ".js",
        "text/javascript": ".js",
        "application/json": ".json",
        "text/html": ".html",
        "text/css": ".css",
        "text/plain": ".txt",
        "image/png": ".png",
        "image/jpeg": ".jpg",
        "image/gif": ".gif",
        "image/svg+xml": ".svg",
        "application/xml": ".xml",
        "application/xhtml+xml": ".xhtml",
    }

    ext = known_extensions.get(mime_type.split(";")[0].strip())
    if ext:
        return ext

    url_ext = Path(url_path).suffix
    if url_ext and len(url_ext) <= 6:
        return url_ext

    return ".bin"


def sanitize_path_component(component: str) -> str:
    """Ersetzt Zeichen, die im Dateisystem problematisch sind."""
    component = re.sub(r"[<>:\"|?*]", "_", component)
    component = component.replace(" ", "_")
    return component or "_"


def build_target_path(extracted_dir: Path, url: str, mime_type: str) -> Path:
    """Baut aus einer URL einen Zielpfad im extracted/-Verzeichnis."""
    parsed = urllib.parse.urlparse(url)

    host = sanitize_path_component(parsed.netloc)
    path = parsed.path

    if not path or path == "/":
        path_parts = ["index"]
        base_name = "index"
    else:
        path_parts = [sanitize_path_component(part) for part in path.split("/") if part]
        base_name = path_parts.pop() if path_parts else "index"

    directory = extracted_dir / host / "/".join(path_parts)

    suffix = Path(base_name).suffix
    if not suffix or suffix == ".":
        base_name = base_name + guess_extension(mime_type, parsed.path)
    else:
        base_name = base_name + guess_extension(mime_type, parsed.path)
        base_name = base_name.rsplit(".", 1)[0] + Path(base_name).suffix

    target = directory / sanitize_path_component(base_name)
    return target


def unique_path(path: Path) -> Path:
    """Fuegt bei Kollisionen eine fortlaufende Nummer ein."""
    if not path.exists():
        return path

    stem = path.stem
    suffix = path.suffix
    counter = 1
    while True:
        candidate = path.with_name(f"{stem}_{counter}{suffix}")
        if not candidate.exists():
            return candidate
        counter += 1


def main() -> None:
    script_dir = Path(__file__).resolve().parent
    har_files = find_har_files(script_dir)

    total_success = 0
    total_skipped = 0

    for har_file in har_files:
        stem = har_file.stem.replace(" ", "_").replace("[", "").replace("]", "")
        extracted_dir = script_dir / f"extracted_{stem}"

        print(f"\nLade HAR-Datei: {har_file.name}")

        with har_file.open("r", encoding="utf-8") as f:
            har_data = json.load(f)

        entries = har_data.get("log", {}).get("entries", [])
        print(f"Gefundene Eintraege: {len(entries)}")

        success_count = 0
        skipped_count = 0

        for entry in entries:
            request = entry.get("request", {})
            response = entry.get("response", {})

            url = request.get("url", "")
            if not url:
                skipped_count += 1
                continue

            content = response.get("content", {})
            mime_type = content.get("mimeType", "")
            text = content.get("text")
            encoding = content.get("encoding", "")

            if text is None:
                skipped_count += 1
                continue

            if encoding == "base64":
                try:
                    data = base64.b64decode(text)
                except Exception:
                    skipped_count += 1
                    continue
            else:
                data = text.encode("utf-8", errors="replace")

            target = build_target_path(extracted_dir, url, mime_type)
            target = unique_path(target)
            target.parent.mkdir(parents=True, exist_ok=True)

            with target.open("wb") as f:
                f.write(data)

            success_count += 1

        print(f"Erfolgreich extrahiert: {success_count}")
        print(f"Uebersprungen: {skipped_count}")
        print(f"Ausgabe-Verzeichnis: {extracted_dir}")
        total_success += success_count
        total_skipped += skipped_count

    print(f"\nGesamt erfolgreich extrahiert: {total_success}")
    print(f"Gesamt uebersprungen: {total_skipped}")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(f"Fehler: {e}", file=sys.stderr)
        sys.exit(1)
