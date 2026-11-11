import http from 'k6/http';
import { check } from 'k6';
export const options={summaryTrendStats:['avg','min','med','max','p(90)','p(95)','p(99)'],scenarios:{orders:{executor:'constant-arrival-rate',rate:Number(__ENV.RATE||50),timeUnit:'1s',duration:__ENV.DURATION||'20s',preAllocatedVUs:10,maxVUs:50}},thresholds:{http_req_failed:['rate<0.01'],http_req_duration:['p(99)<1000']}};
export default function(){const key=__ENV.API_KEY;if(!key)throw new Error('API_KEY required');const body={client_id:`k6-${__ENV.RUN_ID||'run-1'}-${__VU}-${__ITER}`,instrument:['BTC-USD','ETH-USD','SOL-USD'][__ITER%3],side:'BUY',type:'LIMIT',tif:'IOC',price:1,quantity:1};const r=http.post(`${__ENV.BASE_URL||'http://gateway:8080'}/api/orders`,JSON.stringify(body),{headers:{Authorization:`Bearer ${key}`,'Content-Type':'application/json'}});check(r,{'durably committed':r.status===200&&r.json('sequence')>0});}
export function handleSummary(data){return {stdout:JSON.stringify(data,null,2)+'\n'};}
