// 生成 1024x1024 的应用图标源文件（圆角渐变底 + 白色对话气泡 + 三个点），再用 `tauri icon` 生成各尺寸。
import { writeFileSync } from "node:fs";
import { deflateSync } from "node:zlib";

const N = 1024;
const px = Buffer.alloc(N * N * 4);

const lerp = (a, b, t) => Math.round(a + (b - a) * t);
const inRoundRect = (x, y, x0, y0, x1, y1, r) => {
  const cx = Math.min(Math.max(x, x0 + r), x1 - r);
  const cy = Math.min(Math.max(y, y0 + r), y1 - r);
  return (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
};
const inTriangle = (x, y, [ax, ay], [bx, by], [cx, cy]) => {
  const s = (p, q, r) => (p[0] - r[0]) * (q[1] - r[1]) - (q[0] - r[0]) * (p[1] - r[1]);
  const d1 = s([x, y], [ax, ay], [bx, by]), d2 = s([x, y], [bx, by], [cx, cy]), d3 = s([x, y], [cx, cy], [ax, ay]);
  return !((d1 < 0 || d2 < 0 || d3 < 0) && (d1 > 0 || d2 > 0 || d3 > 0));
};

for (let y = 0; y < N; y++) {
  for (let x = 0; x < N; x++) {
    const i = (y * N + x) * 4;
    // 4x 超采样抗锯齿
    let bg = 0, fg = 0, dot = 0;
    for (let sy = 0; sy < 4; sy++) for (let sx = 0; sx < 4; sx++) {
      const X = x + (sx + 0.5) / 4, Y = y + (sy + 0.5) / 4;
      if (inRoundRect(X, Y, 64, 64, 960, 960, 200)) bg++;
      const bubble = inRoundRect(X, Y, 230, 270, 794, 690, 150) || inTriangle(X, Y, [330, 650], [480, 680], [300, 800]);
      if (bubble) {
        const isDot = [372, 512, 652].some((cx) => (X - cx) ** 2 + (Y - 480) ** 2 <= 46 * 46);
        if (isDot) dot++; else fg++;
      }
    }
    const t = (x + y) / (2 * N);
    const [r, g, b] = [lerp(99, 168, t), lerp(102, 85, t), lerp(241, 247, t)]; // indigo → violet
    const a = bg / 16, wf = fg / 16, wd = dot / 16;
    px[i] = Math.round(r * (1 - wf) + 255 * wf);
    px[i + 1] = Math.round(g * (1 - wf) + 255 * wf);
    px[i + 2] = Math.round(b * (1 - wf) + 255 * wf);
    // 点的位置露出底色
    px[i] = Math.round(px[i] * (1 - wd) + r * wd);
    px[i + 1] = Math.round(px[i + 1] * (1 - wd) + g * wd);
    px[i + 2] = Math.round(px[i + 2] * (1 - wd) + b * wd);
    px[i + 3] = Math.round(255 * a);
  }
}

const crcTable = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});
const crc32 = (buf) => {
  let c = 0xffffffff;
  for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
};
const chunk = (type, data) => {
  const len = Buffer.alloc(4); len.writeUInt32BE(data.length);
  const td = Buffer.concat([Buffer.from(type), data]);
  const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td));
  return Buffer.concat([len, td, crc]);
};
const raw = Buffer.alloc((N * 4 + 1) * N);
for (let y = 0; y < N; y++) px.copy(raw, y * (N * 4 + 1) + 1, y * N * 4, (y + 1) * N * 4);
const ihdr = Buffer.alloc(13);
ihdr.writeUInt32BE(N, 0); ihdr.writeUInt32BE(N, 4); ihdr[8] = 8; ihdr[9] = 6;
const png = Buffer.concat([
  Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
  chunk("IHDR", ihdr), chunk("IDAT", deflateSync(raw, { level: 9 })), chunk("IEND", Buffer.alloc(0)),
]);
writeFileSync(process.argv[2] ?? "app-icon.png", png);
