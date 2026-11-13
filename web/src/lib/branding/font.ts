import { Archivo } from "next/font/google"

// The face for branded surfaces (sign-in page, studio preview). One family at
// two widths: narrow for headlines, extra wide for the operator's name.
export const brandFont = Archivo({ subsets: ["latin"], axes: ["wdth"], display: "swap" })
