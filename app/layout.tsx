import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Entire Graph",
  description:
    "Code graph analyzer providing semantic search, symbol definitions, caller-callee relationships, and change impact analysis for coding agents.",
  openGraph: {
    title: "Entire Graph",
    description:
      "Code graph analyzer providing semantic search, symbol definitions, caller-callee relationships, and change impact analysis for coding agents.",
  },
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" className="h-full">
      <body className="min-h-screen bg-neutral-950 text-neutral-100 antialiased font-sans flex flex-col">
        {children}
      </body>
    </html>
  );
}
