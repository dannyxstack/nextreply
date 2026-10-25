// 生成静态网站：把 src/layout.html 套到 src/pages/<lang>/*.html 的正文上，输出到 dist/。
// 无依赖，Node 18+：node build.mjs [--api http://127.0.0.1:8787]
//
// 每个页面文件第一行是元信息注释：<!-- title: …; description: … -->
// 页面和布局里可以用 {{key}} 引用 site.json 与 i18n.json 里的值（如 {{apiUrl}}、{{t.nav.pricing}}）。
// 输出为 <page>/index.html，链接统一写成 /pricing/ 这种形式，任何静态托管都能用干净的地址。

import { readFileSync, writeFileSync, mkdirSync, rmSync, readdirSync, cpSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
const read = (p) => readFileSync(join(root, p), "utf8");
const site = JSON.parse(read("site.json"));
const i18n = JSON.parse(read("src/i18n.json"));
const layout = read("src/layout.html");

// 本地预览时指向本机服务端：node build.mjs --api http://127.0.0.1:8787
const apiArg = process.argv.indexOf("--api");
if (apiArg > 0) site.apiUrl = process.argv[apiArg + 1];

const langs = ["en", "zh"];
// 每种语言的 URL 前缀：英文是默认语言，放在根路径
const prefix = { en: "/", zh: "/zh/" };
const pages = readdirSync(join(root, "src/pages/en")).filter((f) => f.endsWith(".html")).map((f) => f.slice(0, -5));

const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);

function lookup(ctx, key) {
  return key.split(".").reduce((o, k) => (o == null ? undefined : o[k]), ctx);
}

// {{key}} 替换为转义后的值；{{{key}}} 原样插入（用于页面正文等 HTML）
function render(tpl, ctx, where) {
  return tpl
    .replace(/\{\{\{\s*([\w.]+)\s*\}\}\}/g, (_, k) => {
      const v = lookup(ctx, k);
      if (v === undefined) throw new Error(`${where}: unknown {{{${k}}}}`);
      return String(v);
    })
    .replace(/\{\{\s*([\w.]+)\s*\}\}/g, (_, k) => {
      const v = lookup(ctx, k);
      if (v === undefined) throw new Error(`${where}: unknown {{${k}}}`);
      return esc(v);
    });
}

const pageUrl = (lang, page) => prefix[lang] + (page === "index" ? "" : page + "/");

rmSync(join(root, "dist"), { recursive: true, force: true });
cpSync(join(root, "static"), join(root, "dist"), { recursive: true });

for (const lang of langs) {
  const t = i18n[lang];
  for (const page of pages) {
    const src = read(`src/pages/${lang}/${page}.html`);
    const m = src.match(/^<!--\s*title:\s*(.*?);\s*description:\s*(.*?)\s*-->\s*\n/);
    if (!m) throw new Error(`src/pages/${lang}/${page}.html: missing <!-- title: …; description: … --> header`);
    const other = lang === "en" ? "zh" : "en";
    const ctx = {
      ...site,
      t,
      lang,
      page,
      home: prefix[lang],
      title: m[1],
      description: m[2],
      url: site.siteUrl + pageUrl(lang, page),
      altLang: other,
      altUrl: pageUrl(other, page),
      enUrl: site.siteUrl + pageUrl("en", page),
      zhUrl: site.siteUrl + pageUrl("zh", page),
      year: new Date().getFullYear(),
      // 法律页面在主体信息确认前显示"草稿"提示
      draftBanner: site.legalDraft && ["privacy", "terms", "refund"].includes(page) ? `<div class="draft">${esc(t.legal.draft)}</div>` : "",
      contact: site.contactEmail ? `<a href="mailto:${esc(site.contactEmail)}">${esc(site.contactEmail)}</a>` : esc(t.legal.contactTbd),
      winButton: site.downloads.windows.url
        ? `<a class="btn primary" href="${esc(site.downloads.windows.url)}">${esc(t.download.windows)}${site.downloads.windows.version ? ` <small>v${esc(site.downloads.windows.version)}</small>` : ""}</a>`
        : `<span class="btn disabled" aria-disabled="true">${esc(t.download.windows)} · ${esc(t.download.soon)}</span>`,
      nav: Object.fromEntries(["index", "pricing", "download"].map((p) => [p, page === p ? ' aria-current="page"' : ""])),
    };
    const body = render(src.slice(m[0].length), ctx, `${lang}/${page}`);
    const html = render(layout, { ...ctx, body }, "layout");
    const out = join(root, "dist", pageUrl(lang, page), "index.html");
    mkdirSync(dirname(out), { recursive: true });
    writeFileSync(out, html);
  }
}

console.log(`built ${pages.length} pages × ${langs.length} languages → dist/ (api: ${site.apiUrl})`);
