import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "MarketPulse",
  description: "Аналитика продаж маркетплейса и AI-агент поверх MCP",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="ru">
      <body>{children}</body>
    </html>
  );
}
