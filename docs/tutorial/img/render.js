// Renders the tutorial diagrams, src/*.mmd, to *.svg next to this script.
// Mermaid blocks did not render reliably on GitHub, so the chapters embed
// these static SVGs instead; edit the .mmd source and re-run this script.
//
// Usage, from docs/tutorial/img, with Playwright's chromium and mermaid 11:
//   node render.js path/to/mermaid.min.js src .
//
// Labels are SVG text (htmlLabels off), so the images need no foreignObject.
const { chromium } = require('playwright');
const fs = require('fs'), path = require('path');
const [,, mermaidJs, srcDir, outDir] = process.argv;
(async () => {
  const b = await chromium.launch();
  const p = await b.newPage();
  await p.setContent('<html><body></body></html>');
  await p.addScriptTag({ path: mermaidJs });
  await p.evaluate(() => mermaid.initialize({
    startOnLoad: false, theme: 'default', securityLevel: 'strict',
    htmlLabels: false, markdownAutoWrap: false, flowchart: { htmlLabels: false, wrappingWidth: 600 }, fontFamily: 'Helvetica, Arial, sans-serif',
  }));
  for (const f of fs.readdirSync(srcDir).filter(f => f.endsWith('.mmd')).sort()) {
    const code = fs.readFileSync(path.join(srcDir, f), 'utf8');
    const id = 'd' + f.replace(/\W/g, '');
    const svg = await p.evaluate(async ([id, code]) => (await mermaid.render(id, code)).svg, [id, code]);
    // An opaque background keeps the dark lines readable in GitHub's dark theme.
    const [x, y, w, h] = svg.match(/viewBox="([^"]+)"/)[1].split(/\s+/);
    const bg = `<rect x="${x}" y="${y}" width="${w}" height="${h}" fill="#ffffff"/>`;
    // Non-HTML labels escape the entity mermaid made from #quot; a second time.
    const out = svg.replace(/(<svg[^>]*>)/, `$1${bg}`).replaceAll("&amp;quot;", "&quot;");
    fs.writeFileSync(path.join(outDir, f.replace(/\.mmd$/, '.svg')), out + '\n');
    console.log('rendered', f, svg.length);
  }
  await b.close();
})().catch(e => { console.error(e); process.exit(1); });
