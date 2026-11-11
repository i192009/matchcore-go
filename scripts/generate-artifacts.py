"""Regenerate the tracked OpenAPI document and Grafana dashboard (stdlib only)."""
import json
from pathlib import Path

root = Path(__file__).resolve().parent.parent
panels = []
queries = [
    ("Command throughput", "rate(matchcore_commands_total[1m])", "ops"),
    ("Command latency p50 / p95 / p99", None, "s"),
    ("Matching latency", "histogram_quantile(0.99,sum(rate(matchcore_matching_seconds_bucket[1m])) by(le))", "s"),
    ("Database commit p99", "histogram_quantile(0.99,sum(rate(matchcore_commit_seconds_bucket[1m])) by(le))", "s"),
    ("Execution throughput", "rate(matchcore_trades_total[1m])", "ops"),
    ("Rejected orders", 'rate(matchcore_orders_total{outcome="rejected"}[1m])', "ops"),
    ("Queue utilization", "matchcore_queue_depth / 1024", "percentunit"),
    ("Open orders", "matchcore_book_orders", "short"),
    ("Engine recovered / safe", "matchcore_engine_safe", "short"),
    ("Failed transactions", "rate(matchcore_transactions_failed_total[1m])", "ops"),
    ("Outbox backlog", "matchcore_outbox_backlog", "short"),
    ("Publish failures", "rate(matchcore_publish_failures_total[1m])", "ops"),
    ("Delivery p99", "histogram_quantile(0.99,sum(rate(matchcore_event_delay_seconds_bucket[1m])) by(le))", "s"),
    ("Active streams", "matchcore_websocket_clients", "short"),
    ("CPU cores used", "rate(process_cpu_seconds_total[1m])", "short"),
    ("Resident memory", "process_resident_memory_bytes", "bytes"),
    ("Go heap", "go_memstats_heap_alloc_bytes", "bytes"),
    ("Garbage collection pause", "rate(go_gc_duration_seconds_sum[1m])", "s"),
    ("Recovery duration", "matchcore_recovery_seconds_sum", "s"),
]
for i, (title, query, unit) in enumerate(queries):
    targets = [{"expr": query, "refId": "A", "legendFormat": "{{job}} {{instrument}}"}]
    if query is None:
        targets = [{"expr": f"histogram_quantile({p},sum(rate(matchcore_command_seconds_bucket[1m])) by(le))", "refId": chr(65+j), "legendFormat": f"p{round(p*100)}"} for j, p in enumerate([.5,.95,.99])]
    panels.append({"id": i+1,"title":title,"type":"timeseries","datasource":{"type":"prometheus","uid":"prometheus"},"gridPos":{"x":i%3*8,"y":i//3*8,"w":8,"h":8},"targets":targets,"fieldConfig":{"defaults":{"unit":unit}},"options":{"legend":{"displayMode":"list","placement":"bottom"}}})
dashboard={"uid":"matchcore","title":"MatchCore · Engine & Delivery","schemaVersion":41,"version":1,"refresh":"5s","time":{"from":"now-15m","to":"now"},"panels":panels,"tags":["matchcore","simulation"]}
(root/'deployments/grafana/provisioning/dashboards/matchcore.json').write_text(json.dumps(dashboard,indent=2)+'\n',encoding='utf-8')

integer={"type":"integer","format":"int64"}
order={"type":"object","additionalProperties":False,"required":["client_id","instrument","side","type","tif","quantity"],"properties":{"client_id":{"type":"string","maxLength":128},"instrument":{"type":"string","enum":["BTC-USD","ETH-USD","SOL-USD"]},"side":{"type":"string","enum":["BUY","SELL"]},"type":{"type":"string","enum":["LIMIT","MARKET"]},"tif":{"type":"string","enum":["GTC","IOC"]},"price":integer,"quantity":integer,"max_spend":integer}}
paths={}
for route, desc in {"instruments":"List deterministic instrument configurations","book":"Committed aggregated order book","trades":"Most recent 100 public executions","balances":"Authenticated account balances","orders":"Authenticated account order history (200)","order":"One owned order by order_id","operations":"Engine readiness, queue and persistence statistics","audit":"Authenticated account ledger (200)","identity":"Authenticated account and role","account":"Lifetime authenticated account fee, execution and order totals","my-trades":"Most recent 100 owned executions","market":"Disposable Redis last-trade cache; null when empty"}.items():
    paths['/api/'+route]={"get":{"summary":desc,"parameters":[{"name":"instrument","in":"query","schema":{"type":"string"}},{"name":"order_id","in":"query","schema":{"type":"string"}}],"responses":{"200":{"description":"JSON domain document"},"401":{"description":"Authentication required"},"429":{"description":"Rate limited"},"503":{"description":"Service unavailable"}}}}
for route,schema in [('orders',order),('cancel',{"type":"object","additionalProperties":False,"required":["key","instrument","order_id"],"properties":{k:{"type":"string"} for k in ['key','instrument','order_id']}})]:
    paths.setdefault('/api/'+route,{})['post']={"summary":"Submit idempotent "+route+" command","requestBody":{"required":True,"content":{"application/json":{"schema":schema}}},"responses":{"200":{"description":"Committed command envelope (schema_version=1)"},"400":{"description":"Malformed request"},"401":{"description":"Authentication required"},"404":{"description":"Unknown instrument"},"409":{"description":"Idempotency key conflict"},"422":{"description":"Durably recorded business rejection"},"429":{"description":"Rate limit or queue saturation"},"503":{"description":"Engine unsafe, dependency failure or ambiguous commit; retry same key"},"504":{"description":"Deadline; retry same key"}}}
for route in ['healthz','readyz','metrics']:
    paths['/'+route]={"get":{"summary":route,"security":[],"responses":{"200":{"description":"OK"},"503":{"description":"Not ready"}}}}
paths['/ws']={"get":{"summary":"Origin-validated WebSocket upgrade; authenticate in first message within five seconds","security":[],"responses":{"101":{"description":"See docs/api.md for subscribe, snapshot and delta protocol"},"403":{"description":"Origin denied"}}}}
spec={"openapi":"3.0.3","info":{"title":"MatchCore simulation gateway","version":"1.0.0","description":"Quantities: 0.001 coin lots. Price: USD cents per lot ($10 per coin). max_spend and quote balances: cents. All financial fields are integers."},"servers":[{"url":"http://localhost:8080"}],"security":[{"ApiKey":[]}],"components":{"securitySchemes":{"ApiKey":{"type":"http","scheme":"bearer"}}},"paths":paths}
(root/'api/openapi/openapi.json').write_text(json.dumps(spec,indent=2)+'\n',encoding='utf-8')
