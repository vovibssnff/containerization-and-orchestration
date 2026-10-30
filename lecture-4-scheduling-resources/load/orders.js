import http from "k6/http";
import { check } from "k6";

export const options = {
  vus: 5,
  duration: __ENV.DURATION || "3m",
  thresholds: {
    "http_req_failed{name:order}": ["rate<0.01"],
    "http_req_duration{name:order}": ["p(95)<300"],
  },
};

export default function () {
  const res = http.post(
    `${__ENV.BASE}/order`,
    JSON.stringify({ item: "bread" }),
    { headers: { "Content-Type": "application/json" }, tags: { name: "order" } },
  );
  check(res, { "order stored": (r) => r.status === 201 });
}
