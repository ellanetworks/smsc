import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ThemeProvider, useTheme } from "@mui/material/styles";
import theme, { THEME_PROVIDER_PROPS } from "@/utils/theme";
import { dark, light } from "@/utils/tokens";

const Probe = () => {
  const t = useTheme();
  return (
    <>
      <span data-testid="primary">{t.palette.primary.main}</span>
      <span data-testid="link">{t.palette.link}</span>
      <span data-testid="info">{t.palette.info.main}</span>
      <span data-testid="subtle">{t.palette.backgroundSubtle}</span>
      <span data-testid="canvas">{t.palette.background.default}</span>
      <span data-testid="paper">{t.palette.background.paper}</span>
      <span data-testid="text">{t.palette.text.primary}</span>
      <span data-testid="headerBg">{t.palette.DataGrid.headerBg}</span>
      <span data-testid="mode">{t.palette.mode}</span>
    </>
  );
};

const renderIn = (mode: "light" | "dark") =>
  render(
    <ThemeProvider
      theme={theme}
      {...THEME_PROVIDER_PROPS}
      defaultMode={mode}
      storageManager={null}
    >
      <Probe />
    </ThemeProvider>,
  );

const value = (id: string) => screen.getByTestId(id).textContent;

const relativeLuminance = (hex: string) => {
  const channels = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
};

const contrast = (a: string, b: string) => {
  const [hi, lo] = [relativeLuminance(a), relativeLuminance(b)].sort(
    (x, y) => y - x,
  );
  return (hi + 0.05) / (lo + 0.05);
};

const WCAG_AA = 4.5;

describe("theme color schemes", () => {
  it.each([
    ["light", light],
    ["dark", dark],
  ] as const)("resolves the %s tokens", (mode, tokens) => {
    renderIn(mode);

    expect(value("mode")).toBe(mode);
    expect(value("primary")).toBe(tokens.primary);
    expect(value("link")).toBe(tokens.link);
    expect(value("info")).toBe(tokens.info);
    expect(value("subtle")).toBe(tokens.backgroundSubtle);
    expect(value("canvas")).toBe(tokens.backgroundDefault);
    expect(value("paper")).toBe(tokens.backgroundPaper);
    expect(value("text")).toBe(tokens.textPrimary);
    expect(value("headerBg")).toBe(tokens.backgroundSubtle);
  });
});

describe("palette contrast", () => {
  it("keeps the dark accents readable on every dark surface", () => {
    for (const color of [
      dark.primary,
      dark.success,
      dark.error,
      dark.warning,
      dark.info,
      dark.link,
    ]) {
      for (const surface of [
        dark.backgroundDefault,
        dark.backgroundPaper,
        dark.backgroundSubtle,
      ]) {
        expect(contrast(color, surface)).toBeGreaterThanOrEqual(WCAG_AA);
      }
    }
  });

  it("keeps the light accents readable on the page", () => {
    for (const color of [
      light.primary,
      light.success,
      light.error,
      light.info,
      light.link,
    ]) {
      expect(contrast(color, light.backgroundDefault)).toBeGreaterThanOrEqual(
        WCAG_AA,
      );
    }
  });

  it("keeps body text readable on every dark surface", () => {
    for (const surface of [
      dark.backgroundDefault,
      dark.backgroundPaper,
      dark.backgroundSubtle,
    ]) {
      expect(contrast(dark.textPrimary, surface)).toBeGreaterThanOrEqual(7);
    }
  });
});
