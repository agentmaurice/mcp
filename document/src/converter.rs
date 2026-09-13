use anydoc::{ConvertError, Format};
use bytes::Bytes;
use sha2::{Digest, Sha256};
use std::path::Path;

#[derive(Debug)]
pub struct Conversion {
    pub markdown: String,
    pub format: &'static str,
    pub sha256: String,
}

pub fn convert(bytes: &Bytes, filename: Option<&str>) -> Result<Conversion, ConvertError> {
    let format = detect_format(bytes, filename)?;
    let markdown = anydoc::to_markdown_bytes(bytes, format)?;
    let sha256 = format!("{:x}", Sha256::digest(bytes));
    Ok(Conversion {
        markdown,
        format: format_name(format),
        sha256,
    })
}

fn detect_format(bytes: &[u8], filename: Option<&str>) -> Result<Format, ConvertError> {
    Format::from_bytes(bytes)
        .or_else(|| filename.and_then(|name| Format::from_path(Path::new(name))))
        .ok_or_else(|| {
            ConvertError::Unsupported(
                "unrecognized file content; provide a filename with a supported extension".into(),
            )
        })
}

fn format_name(format: Format) -> &'static str {
    match format {
        Format::Doc => "doc",
        Format::Docx => "docx",
        Format::Odt => "odt",
        Format::Pdf => "pdf",
        Format::Ppt => "ppt",
        Format::Pptx => "pptx",
        Format::Rtf => "rtf",
        Format::Epub => "epub",
        Format::Excel => "excel",
        Format::Ods => "ods",
        Format::Odp => "odp",
        Format::Csv => "csv",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn converts_csv_when_filename_names_the_format() {
        let bytes = Bytes::from_static(b"name,score\nAlice,42\n");
        let result = convert(&bytes, Some("scores.csv")).unwrap();
        assert_eq!(result.format, "csv");
        assert!(result.markdown.contains("Alice"));
        assert_eq!(result.sha256.len(), 64);
    }

    #[test]
    fn detects_rtf_from_bytes() {
        let bytes = Bytes::from_static(br"{\rtf1\ansi Hello document}");
        let result = convert(&bytes, None).unwrap();
        assert_eq!(result.format, "rtf");
        assert!(result.markdown.contains("Hello document"));
    }
}
