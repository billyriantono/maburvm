import { Archivo, IBM_Plex_Mono } from "next/font/google"

// One family for the whole product. Its width axis does the work a second
// typeface would: normal for reading, wide for a machine's or operator's name.
export const brandFont = Archivo({ subsets: ["latin"], axes: ["wdth"], display: "swap", variable: "--font-sans" })

// Only for strings people copy (addresses, commands, keys), where 0/O and 1/l
// have to look different.
export const monoFont = IBM_Plex_Mono({ subsets: ["latin"], weight: ["400", "500"], display: "swap", variable: "--font-mono" })
