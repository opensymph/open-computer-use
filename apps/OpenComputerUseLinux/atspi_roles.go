package main

// Code generated from at-spi2-core/at-spi2-atk role tables. Do not edit by
// hand. atspiRoleNames maps an AtspiRole enum value (as carried by the
// AT-SPI2 cache) to the role-name string a GetRoleName call would return
// for the same object: ATK-bridged roles resolve through the at-spi2-atk
// role mapping into atk_role_get_name strings; AT-SPI-only roles use the
// libatspi enum nick with hyphens compacted to spaces.
var atspiRoleNames = []string{
	0:   "invalid",               // ATSPI_ROLE_INVALID
	1:   "accelerator label",     // ATSPI_ROLE_ACCELERATOR_LABEL
	2:   "alert",                 // ATSPI_ROLE_ALERT
	3:   "animation",             // ATSPI_ROLE_ANIMATION
	4:   "arrow",                 // ATSPI_ROLE_ARROW
	5:   "calendar",              // ATSPI_ROLE_CALENDAR
	6:   "canvas",                // ATSPI_ROLE_CANVAS
	7:   "check box",             // ATSPI_ROLE_CHECK_BOX
	8:   "check menu item",       // ATSPI_ROLE_CHECK_MENU_ITEM
	9:   "color chooser",         // ATSPI_ROLE_COLOR_CHOOSER
	10:  "column header",         // ATSPI_ROLE_COLUMN_HEADER
	11:  "combo box",             // ATSPI_ROLE_COMBO_BOX
	12:  "date editor",           // ATSPI_ROLE_DATE_EDITOR
	13:  "desktop icon",          // ATSPI_ROLE_DESKTOP_ICON
	14:  "desktop frame",         // ATSPI_ROLE_DESKTOP_FRAME
	15:  "dial",                  // ATSPI_ROLE_DIAL
	16:  "dialog",                // ATSPI_ROLE_DIALOG
	17:  "directory pane",        // ATSPI_ROLE_DIRECTORY_PANE
	18:  "drawing area",          // ATSPI_ROLE_DRAWING_AREA
	19:  "file chooser",          // ATSPI_ROLE_FILE_CHOOSER
	20:  "filler",                // ATSPI_ROLE_FILLER
	21:  "focus traversable",     // ATSPI_ROLE_FOCUS_TRAVERSABLE
	22:  "font chooser",          // ATSPI_ROLE_FONT_CHOOSER
	23:  "frame",                 // ATSPI_ROLE_FRAME
	24:  "glass pane",            // ATSPI_ROLE_GLASS_PANE
	25:  "html container",        // ATSPI_ROLE_HTML_CONTAINER
	26:  "icon",                  // ATSPI_ROLE_ICON
	27:  "image",                 // ATSPI_ROLE_IMAGE
	28:  "internal frame",        // ATSPI_ROLE_INTERNAL_FRAME
	29:  "label",                 // ATSPI_ROLE_LABEL
	30:  "layered pane",          // ATSPI_ROLE_LAYERED_PANE
	31:  "list",                  // ATSPI_ROLE_LIST
	32:  "list item",             // ATSPI_ROLE_LIST_ITEM
	33:  "menu",                  // ATSPI_ROLE_MENU
	34:  "menu bar",              // ATSPI_ROLE_MENU_BAR
	35:  "menu item",             // ATSPI_ROLE_MENU_ITEM
	36:  "option pane",           // ATSPI_ROLE_OPTION_PANE
	37:  "page tab",              // ATSPI_ROLE_PAGE_TAB
	38:  "page tab list",         // ATSPI_ROLE_PAGE_TAB_LIST
	39:  "panel",                 // ATSPI_ROLE_PANEL
	40:  "password text",         // ATSPI_ROLE_PASSWORD_TEXT
	41:  "popup menu",            // ATSPI_ROLE_POPUP_MENU
	42:  "progress bar",          // ATSPI_ROLE_PROGRESS_BAR
	43:  "push button",           // ATSPI_ROLE_BUTTON
	44:  "radio button",          // ATSPI_ROLE_RADIO_BUTTON
	45:  "radio menu item",       // ATSPI_ROLE_RADIO_MENU_ITEM
	46:  "root pane",             // ATSPI_ROLE_ROOT_PANE
	47:  "row header",            // ATSPI_ROLE_ROW_HEADER
	48:  "scroll bar",            // ATSPI_ROLE_SCROLL_BAR
	49:  "scroll pane",           // ATSPI_ROLE_SCROLL_PANE
	50:  "separator",             // ATSPI_ROLE_SEPARATOR
	51:  "slider",                // ATSPI_ROLE_SLIDER
	52:  "spin button",           // ATSPI_ROLE_SPIN_BUTTON
	53:  "split pane",            // ATSPI_ROLE_SPLIT_PANE
	54:  "statusbar",             // ATSPI_ROLE_STATUS_BAR
	55:  "table",                 // ATSPI_ROLE_TABLE
	56:  "table cell",            // ATSPI_ROLE_TABLE_CELL
	57:  "table column header",   // ATSPI_ROLE_TABLE_COLUMN_HEADER
	58:  "table row header",      // ATSPI_ROLE_TABLE_ROW_HEADER
	59:  "tear off menu item",    // ATSPI_ROLE_TEAROFF_MENU_ITEM
	60:  "terminal",              // ATSPI_ROLE_TERMINAL
	61:  "text",                  // ATSPI_ROLE_TEXT
	62:  "toggle button",         // ATSPI_ROLE_TOGGLE_BUTTON
	63:  "tool bar",              // ATSPI_ROLE_TOOL_BAR
	64:  "tool tip",              // ATSPI_ROLE_TOOL_TIP
	65:  "tree",                  // ATSPI_ROLE_TREE
	66:  "tree table",            // ATSPI_ROLE_TREE_TABLE
	67:  "unknown",               // ATSPI_ROLE_UNKNOWN
	68:  "viewport",              // ATSPI_ROLE_VIEWPORT
	69:  "window",                // ATSPI_ROLE_WINDOW
	70:  "extended",              // ATSPI_ROLE_EXTENDED
	71:  "header",                // ATSPI_ROLE_HEADER
	72:  "footer",                // ATSPI_ROLE_FOOTER
	73:  "paragraph",             // ATSPI_ROLE_PARAGRAPH
	74:  "ruler",                 // ATSPI_ROLE_RULER
	75:  "application",           // ATSPI_ROLE_APPLICATION
	76:  "autocomplete",          // ATSPI_ROLE_AUTOCOMPLETE
	77:  "edit bar",              // ATSPI_ROLE_EDITBAR
	78:  "embedded",              // ATSPI_ROLE_EMBEDDED
	79:  "entry",                 // ATSPI_ROLE_ENTRY
	80:  "chart",                 // ATSPI_ROLE_CHART
	81:  "caption",               // ATSPI_ROLE_CAPTION
	82:  "document frame",        // ATSPI_ROLE_DOCUMENT_FRAME
	83:  "heading",               // ATSPI_ROLE_HEADING
	84:  "page",                  // ATSPI_ROLE_PAGE
	85:  "section",               // ATSPI_ROLE_SECTION
	86:  "redundant object",      // ATSPI_ROLE_REDUNDANT_OBJECT
	87:  "form",                  // ATSPI_ROLE_FORM
	88:  "link",                  // ATSPI_ROLE_LINK
	89:  "input method window",   // ATSPI_ROLE_INPUT_METHOD_WINDOW
	90:  "table row",             // ATSPI_ROLE_TABLE_ROW
	91:  "tree item",             // ATSPI_ROLE_TREE_ITEM
	92:  "document spreadsheet",  // ATSPI_ROLE_DOCUMENT_SPREADSHEET
	93:  "document presentation", // ATSPI_ROLE_DOCUMENT_PRESENTATION
	94:  "document text",         // ATSPI_ROLE_DOCUMENT_TEXT
	95:  "document web",          // ATSPI_ROLE_DOCUMENT_WEB
	96:  "document email",        // ATSPI_ROLE_DOCUMENT_EMAIL
	97:  "comment",               // ATSPI_ROLE_COMMENT
	98:  "list box",              // ATSPI_ROLE_LIST_BOX
	99:  "grouping",              // ATSPI_ROLE_GROUPING
	100: "image map",             // ATSPI_ROLE_IMAGE_MAP
	101: "notification",          // ATSPI_ROLE_NOTIFICATION
	102: "info bar",              // ATSPI_ROLE_INFO_BAR
	103: "level bar",             // ATSPI_ROLE_LEVEL_BAR
	104: "title bar",             // ATSPI_ROLE_TITLE_BAR
	105: "block quote",           // ATSPI_ROLE_BLOCK_QUOTE
	106: "audio",                 // ATSPI_ROLE_AUDIO
	107: "video",                 // ATSPI_ROLE_VIDEO
	108: "definition",            // ATSPI_ROLE_DEFINITION
	109: "article",               // ATSPI_ROLE_ARTICLE
	110: "landmark",              // ATSPI_ROLE_LANDMARK
	111: "log",                   // ATSPI_ROLE_LOG
	112: "marquee",               // ATSPI_ROLE_MARQUEE
	113: "math",                  // ATSPI_ROLE_MATH
	114: "rating",                // ATSPI_ROLE_RATING
	115: "timer",                 // ATSPI_ROLE_TIMER
	116: "static",                // ATSPI_ROLE_STATIC
	117: "math fraction",         // ATSPI_ROLE_MATH_FRACTION
	118: "math root",             // ATSPI_ROLE_MATH_ROOT
	119: "subscript",             // ATSPI_ROLE_SUBSCRIPT
	120: "superscript",           // ATSPI_ROLE_SUPERSCRIPT
	121: "description list",      // ATSPI_ROLE_DESCRIPTION_LIST
	122: "description term",      // ATSPI_ROLE_DESCRIPTION_TERM
	123: "description value",     // ATSPI_ROLE_DESCRIPTION_VALUE
	124: "footnote",              // ATSPI_ROLE_FOOTNOTE
	125: "content deletion",      // ATSPI_ROLE_CONTENT_DELETION
	126: "content insertion",     // ATSPI_ROLE_CONTENT_INSERTION
	127: "mark",                  // ATSPI_ROLE_MARK
	128: "suggestion",            // ATSPI_ROLE_SUGGESTION
	129: "push button menu",      // ATSPI_ROLE_PUSH_BUTTON_MENU
	130: "switch",                // ATSPI_ROLE_SWITCH
}
