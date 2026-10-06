// 清空本地 wrangler dev 的 KV 计数（按 IP 的注册 / 体验额度 / 验证码 / 请求限额）。
// 本地所有请求的 IP 都是 127.0.0.1，跑过几次测试后很快会撞上每日上限；只影响本地，不碰账号和积分数据。
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

// 直接用当前 node 运行 wrangler，不经过 shell（Linux / Windows 行为一致）
const wranglerBin = fileURLToPath(new URL("../node_modules/wrangler/bin/wrangler.js", import.meta.url));
const wrangler = (args) => execFileSync(process.execPath, [wranglerBin, ...args], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] });

const keys = JSON.parse(wrangler(["kv", "key", "list", "--binding", "QUOTA", "--local"])).map((k) => k.name);
for (const key of keys) {
  wrangler(["kv", "key", "delete", "--binding", "QUOTA", "--local", key]);
  console.log(`deleted ${key}`);
}
console.log(keys.length ? `cleared ${keys.length} keys` : "nothing to clear");
