import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';

const root = process.cwd();
const sourceRoot = path.join(root, 'src');
const allowedTokenFiles = new Set([
  path.join(sourceRoot, 'design', 'tokens.css'),
  path.join(sourceRoot, 'design', 'tokens.ts'),
]);
const sourceExtensions = new Set(['.astro', '.css', '.js', '.mjs', '.ts']);
const ignoredDirs = new Set(['node_modules', 'dist', '.astro']);

const tokenClassPattern = /\b(?:bg|text|border|ring|shadow|rounded)-\[[^\]]+\]/g;
const rawColorPattern = /#[0-9a-fA-F]{3,8}(?![0-9a-fA-Fa-zA-Z_-])|rgba?\(|hsla?\(|oklch\(|cubic-bezier\(/g;
const styleAttributePattern = /\bstyle\s*=/g;

const isAllowedRadius = (value) => (
  value.startsWith('var(') ||
  value === 'inherit' ||
  value === '50%'
);

const isAllowedShadow = (value) => (
  value.startsWith('var(') ||
  value === 'none'
);

const collectFiles = async (dir) => {
  const entries = await readdir(dir, { withFileTypes: true });
  const files = [];

  for (const entry of entries) {
    if (entry.isDirectory()) {
      if (!ignoredDirs.has(entry.name)) {
        files.push(...await collectFiles(path.join(dir, entry.name)));
      }
      continue;
    }

    if (sourceExtensions.has(path.extname(entry.name))) {
      files.push(path.join(dir, entry.name));
    }
  }

  return files;
};

const reportMatches = (violations, file, line, pattern, message) => {
  const matches = [...line.matchAll(pattern)];
  for (const match of matches) {
    violations.push({ file, message, value: match[0] });
  }
};

const lintFile = async (file) => {
  if (allowedTokenFiles.has(file)) return [];

  const text = await readFile(file, 'utf8');
  const violations = [];

  text.split('\n').forEach((line, index) => {
    const lineNumber = index + 1;
    const location = `${path.relative(root, file)}:${lineNumber}`;

    reportMatches(violations, location, line, rawColorPattern, 'Use design tokens instead of raw color/easing literals.');
    reportMatches(violations, location, line, tokenClassPattern, 'Use named Tailwind tokens instead of arbitrary token classes.');
    reportMatches(violations, location, line, styleAttributePattern, 'Move inline styles into tokenized Tailwind/CSS classes.');
    const radiusMatch = line.match(/border-radius\s*:\s*([^;]+)/);
    if (radiusMatch && !isAllowedRadius(radiusMatch[1].trim())) {
      violations.push({
        file: location,
        message: 'Use radius tokens instead of inline border radii.',
        value: radiusMatch[0],
      });
    }

    const shadowMatch = line.match(/box-shadow\s*:\s*([^;]+)/);
    if (shadowMatch && !isAllowedShadow(shadowMatch[1].trim())) {
      violations.push({
        file: location,
        message: 'Use shadow tokens instead of inline shadows.',
        value: shadowMatch[0],
      });
    }

    const fontFamilyMatch = line.match(/font-family\s*:\s*([^;]+)/);
    if (fontFamilyMatch && !fontFamilyMatch[1].trim().startsWith('var(')) {
      violations.push({
        file: location,
        message: 'Use font tokens instead of inline font stacks.',
        value: fontFamilyMatch[0],
      });
    }
  });

  return violations;
};

const files = await collectFiles(sourceRoot);
const violations = (await Promise.all(files.map(lintFile))).flat();

if (violations.length > 0) {
  console.error('Design token lint failed:');
  for (const violation of violations) {
    console.error(`- ${violation.file}: ${violation.message} (${violation.value})`);
  }
  process.exit(1);
}

console.log('Design token lint passed.');
