"""Capture report screenshots from committed results, without rerunning tests.

Requires Python Playwright and its Chromium browser. All displayed measurements
are read from the source artifacts linked in the report.
"""

import argparse
import html
import json
import re
from pathlib import Path

from playwright.sync_api import sync_playwright


ROOT = Path(__file__).resolve().parent.parent
RESULTS = ROOT / "benchmarks" / "results"


def read_json(name):
    return json.loads((RESULTS / name).read_text(encoding="utf-8-sig"))


def escape(value):
    return html.escape(str(value))


def card(label, value, detail=""):
    return (
        f'<div class="card"><span>{escape(label)}</span>'
        f'<strong>{escape(value)}</strong><small>{escape(detail)}</small></div>'
    )


def source(name, label=None):
    return f'<a href="../../benchmarks/results/{escape(name)}">{escape(label or name)}</a>'


def latency(us):
    return f"{us / 1000:.2f} ms" if us >= 1000 else f"{us:.2f} us"


def report(title, subtitle, content, sources, captured, report_id):
    return f"""<section class="report" id="{report_id}">
      <header><b>MATCHCORE</b><span>RECORDED ENGINEERING RESULTS</span></header>
      <p class="eyebrow">VERIFICATION / SIMULATED EXCHANGE</p>
      <h1>{escape(title)}</h1><p class="subtitle">{escape(subtitle)}</p>
      {content}
      <div class="sources"><b>Source artifacts</b><p>{sources}</p></div>
      <footer><span>Captured {escape(captured)} · Recorded outputs, not a new test run</span>
      <span>Internal simulated assets only</span></footer>
    </section>"""


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--capture-date", required=True, help="Actual YYYY-MM-DD capture date")
    args = parser.parse_args()
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", args.capture_date):
        parser.error("capture date must use YYYY-MM-DD")

    events = [json.loads(line) for line in (RESULTS / "tests-go.jsonl").read_text().splitlines()]
    named = [event for event in events if event.get("Test")]
    passed = [event for event in named if event["Action"] == "pass"]
    failed = [event for event in named if event["Action"] == "fail"]
    skipped = [event for event in named if event["Action"] == "skip"]
    targets = [event for event in passed if "/" not in event["Test"]]
    recorded = events[0]["Time"][:10]
    browser_log = (RESULTS / "browser-tests.txt").read_text(encoding="utf-8-sig")
    browser_counts = re.findall(r"(\d+) passed", browser_log)
    if not browser_counts:
        raise ValueError("Browser result artifact has no passing-test count")
    browser_count = int(browser_counts[-1])
    acceptance = (RESULTS / "acceptance.txt").read_text(encoding="utf-8-sig")
    acceptance_passes = sum(line.startswith("PASS:") for line in acceptance.splitlines())
    npm_vulnerabilities = read_json("security-npm.json")["metadata"]["vulnerabilities"]["total"]
    go_scan = (RESULTS / "security-go-final.txt").read_text()
    go_scan_status = "No vulnerabilities found" if "No vulnerabilities found." in go_scan else "Inspect source result"
    verification_content = '<div class="cards">' + "".join([
        card("Go test/fuzz targets", len(targets), "Top-level targets including fuzz seeds' parent"),
        card("Named passing entries", len(passed), "Includes parent tests and nested subtests"),
        card("Browser tests passed", browser_count, "Real order entry, cancel, mobile and Grafana"),
        card("Named failures / skips", f"{len(failed)} / {len(skipped)}", "Packages without test files are not counted"),
    ]) + '</div><div class="checks">' + "".join([
        f'<p><b>Actual-service acceptance</b><span>{acceptance_passes} recorded PASS stages: trade/settlement, restart/idempotency</span></p>',
        f'<p><b>Go dependency scan</b><span>{escape(go_scan_status)}</span></p>',
        f'<p><b>npm audit</b><span>{npm_vulnerabilities} reported vulnerabilities</span></p>',
        '<p><b>Financial correctness</b><span>Price-time priority, reservations, balanced settlement and deterministic replay</span></p>',
    ]) + '</div><p class="note">The recorded Go invocation included the race detector and real PostgreSQL, Redis and NATS integration checks. Detailed guarantees and limits remain in docs/verification.md.</p>'

    workloads = [
        ("Pure matching", "memory-sustained.json"),
        ("Actor + PostgreSQL", "durable-single.json"),
        ("Shared-account contention", "durable-multi.json"),
        ("HTTP + gRPC + durable commit", "e2e.json"),
    ]
    rows = []
    for label, filename in workloads:
        sample = read_json(filename)
        values = [label, f'{sample["orders"]:,}', f'{sample["orders_per_second"]:,.1f}',
                  latency(sample["p50_us"]), latency(sample["p95_us"]), latency(sample["p99_us"]),
                  f'{sample["seconds"]:.3f} s', sample["failures"]]
        rows.append("<tr>" + "".join(f"<td>{escape(value)}</td>" for value in values) + "</tr>")
    durable = read_json("durable-single.json")
    api = read_json("e2e.json")
    benchmark_content = '<div class="cards two">' + card(
        "Durable engine", f'{durable["orders_per_second"]:.1f} orders/s',
        "Includes real risk checks, SQL settlement and commits"
    ) + card("API to commit", f'{api["orders_per_second"]:.1f} orders/s',
             "Includes HTTP, authentication, gRPC and durability") + '</div>'
    benchmark_content += '<table><thead><tr>' + "".join(
        f"<th>{label}</th>" for label in ["Measurement boundary", "Orders", "Orders/s", "p50", "p95", "p99", "Duration", "Failed"]
    ) + '</tr></thead><tbody>' + "".join(rows) + '</tbody></table>'
    benchmark_content += '<p class="note">Short development samples on an 8-core Snapdragon X / Linux ARM64 Docker environment, Go 1.26.11. All rows used a 50/50 crossing/non-crossing IOC mix. Memory matching excludes persistence and networking; the contention sample shares financial account locks. These rows are different boundaries, not interchangeable capacity claims.</p>'

    ci = read_json("github-ci.json")
    ci_content = '<div class="cards two">' + card(
        "Hosted workflow", ci["conclusion"].upper(), ci["name"]
    ) + card("Source commit", ci["head_sha"][:7], "The exact code revision verified by this run") + '</div><div class="checks">'
    for job in ci["jobs"]:
        ci_content += f'<p><b>{escape(job["name"])}</b><span>{escape(job["conclusion"].upper())} · {escape(job["completed_at"])}</span></p>'
    ci_content += f'</div><p class="note">The hosted run built and tested the backend, checked the frontend, started the real Docker services, executed the trade/restart acceptance flow, and ran browser checks. This snapshot describes the linked recorded run; later commits can have their own workflow results.</p><p class="run-link">{escape(ci["url"])}</p>'

    sections = [
        report("Verification results", f"Recorded local verification: {recorded}", verification_content,
               " · ".join(source(name) for name in ["tests-go.jsonl", "browser-tests.txt", "acceptance.txt", "security-go-final.txt", "security-npm.json"]), args.capture_date, "verification"),
        report("Measured performance", "Recorded local baselines: 2026-11-11 · prices and balances are simulated", benchmark_content,
               " · ".join(source(filename) for _, filename in workloads), args.capture_date, "benchmarks"),
        report("GitHub Actions results", f'Hosted run recorded {ci["updated_at"][:10]} · checked {ci["checked_on"]}', ci_content,
               source("github-ci.json") + f' · <a href="{escape(ci["url"])}">Open the actual hosted workflow</a>', args.capture_date, "ci"),
    ]
    css = """
      *{box-sizing:border-box}body{margin:0;padding:28px;background:#080e15;color:#e5edf4;font:15px Arial,sans-serif}
      .report{width:1280px;margin:0 auto 28px;padding:40px 44px;background:#101923;border:1px solid #2b3c4c;border-radius:12px}
      header,footer{display:flex;justify-content:space-between;align-items:center}header{border-bottom:1px solid #2b3c4c;padding-bottom:24px;letter-spacing:2px}header b{color:#51dbb3;font-size:18px}header span{font-size:11px;color:#8fa3b7}
      .eyebrow{font-size:11px;letter-spacing:2px;color:#7e9bb2;margin-top:32px}h1{font-size:38px;letter-spacing:-1px;margin:10px 0 12px}.subtitle{color:#9aafc0;margin:0 0 28px}
      .cards{display:grid;grid-template-columns:repeat(4,1fr);gap:14px;margin-bottom:24px}.cards.two{grid-template-columns:1fr 1fr}.card{padding:21px;background:#14232f;border:1px solid #2d4354;border-radius:7px}.card span{display:block;color:#a8bbca;font-size:13px}.card strong{display:block;font:30px Consolas,monospace;color:#58e1b6;margin:12px 0}.card small{display:block;color:#91a6b9;line-height:1.5;font-size:12px}
      .checks{border:1px solid #2b3c4c;border-radius:7px;padding:0 20px}.checks p{display:grid;grid-template-columns:260px 1fr;gap:16px;padding:17px 0;margin:0;border-bottom:1px solid #253746}.checks p:last-child{border:0}.checks b{font-weight:500}.checks span{color:#a9bdcc;line-height:1.45}
      table{border-collapse:collapse;width:100%;font-size:13px}th{text-align:left;color:#91a9bc;background:#152431;font-weight:500}th,td{padding:17px 10px;border-bottom:1px solid #2b3c4c;white-space:nowrap}td:not(:first-child){font-family:Consolas,monospace}td:first-child{max-width:240px;white-space:normal}
      .note{color:#a0b3c3;line-height:1.65;margin:24px 0;font-size:13px}.sources{padding-top:18px;border-top:1px solid #2b3c4c;font-size:12px}.sources b{color:#a9bdcc;font-weight:500}.sources p{line-height:1.9}a{color:#5ed5b3;text-decoration:none}.run-link{font:13px Consolas,monospace;color:#65dbb9;overflow-wrap:anywhere}
      footer{font-size:11px;color:#7890a6;border-top:1px solid #2b3c4c;margin-top:22px;padding-top:22px}
    """
    document = f'<!doctype html><html lang="en"><meta charset="utf-8"><title>MatchCore recorded results</title><style>{css}</style><body>{"".join(sections)}</body></html>'
    output = ROOT / "screenshots" / "results"
    output.mkdir(parents=True, exist_ok=True)
    (output / "index.html").write_text(document, encoding="utf-8", newline="\n")
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True)
        page = browser.new_page(viewport={"width": 1360, "height": 1100}, device_scale_factor=1)
        page.set_content(document, wait_until="load")
        for section, filename in [("verification", "verification-results.png"),
                                  ("benchmarks", "benchmark-results.png"),
                                  ("ci", "github-actions.png")]:
            page.locator(f"#{section}").screenshot(path=str(output / filename))
            print(f"Captured {output.relative_to(ROOT) / filename}")
        browser.close()


if __name__ == "__main__":
    main()
