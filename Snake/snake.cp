#include	<cstdio>		// basics
#include	<cstring>		// String funcs
#include	<ctime>			// Time
#include	<iostream>		// Debug output

#include	<stdlib.h>
#include	<termios.h>

#include	<ncurses.h>		// Text screen
#include	<sys/time.h>	// gettimeofday
#include	<unistd.h>		// usleep

using namespace std;


///////////////////////////////////////////////////////////////////////////////
//
//		(c)2014	Akua Inc., BSD Licensed
//
//////////////////////////////////////////////////
//
//		Snake game for terminals
//		Tested compile on Linux, Mac, FreeBSD
//
//////////////////////////////////////////////////
//
// History
//	Ver	Who	When	What
//	002	GLK	141005	Animate apple
//	001	GLK	141004	Fix Apple landing on snake then being erased by snake move
//
///////////////////////////////////////////////////////////////////////////////
#define		VERSION		0x00020000
#define		BUILDDATE	0x20141004
///////////////////////////////////////////////////////////////////////////////


enum Justification
 {
	Left = -1,
	Center = 0,
	Right = 1
 };


enum SpriteNum	// Offsets into the Sprite string for text mode
 {
	BlankSprite = 0,
	SnakeNeckSprite = 1,
	AppleSprite = 2,
	SnakeHeadSpriteN = 3,
	SnakeHeadSpriteS = 4,
	SnakeHeadSpriteE = 5,
	SnakeHeadSpriteW = 6
 };


enum SizesOfThings
 {
	MessageSize = 100,		// Largest stored message
	TickleSpeed = 24676,	// Microseconds of sleep between loops
	AnimationSpeed = 5		// Smaller is faster
 };


static char Sprites[] = " O*v^<>";
static char Twirly[] = "-\\])|([/+";
static char Applets[] = "*#@#";


class Timer
 {
  private:
	struct timeval	now;				// Updating timeval each time we loop
	long			ms;
	time_t			base;

  public:
	Timer()			{ Update(); base = now.tv_sec; }

	void Update()	{ gettimeofday(&now, NULL);			// Update timer regularly
						ms = ((now.tv_sec - base) * 1000) + (now.tv_usec / 1000); }
	time_t MS()		{ return ms; }						// Millisecond timer since launch of program
 };


class Point				// Just a point on a plane
 {
  public:
	int	x;
	int	y;
 };


class Rect				// Top left and bottom right of a rectangle
 {
  public:
  	Point	tl;
  	Point	br;

	// Basic rectangle functions
	Rect() {};
	Rect(int l, int t, int r, int b) { tl.x = l; tl.y = t; br.x = r; br.y = b; };

	// Top, Left, Bottom, Right, Width, Height
	int T()	const	{ return tl.y; }
	int L()	const	{ return tl.x; }
	int B()	const	{ return br.y; }
	int R() const	{ return br.x; }
	int W()	const	{ return br.x - tl.x; }
	int H()	const	{ return br.y - tl.y; }
 };


class Points			// A list of points (one point and a pointer to the next one or NULL if the end of the list
 {
  private:
	Point		loc;
	Points *	next;

  public:
	Points(Points *tail, Point * p) { next = tail; loc = *p; }		// A new head up front
	Points(Point * p)		{ next = 0; loc = *p; }					// Start at a point
	Points(int x, int y)	{ next = 0; loc.x = x; loc.y = y; }		// Start with an x,y
	~Points()				{ if (next) delete next; }				// Remove child before we die
	Point P()				{ return loc; }
	int X()					{ return loc.x; }
	int Y()					{ return loc.y; }
	Points *	Next()		{ return next; }

	Points **	Tail();
	int	Length();
 };


class Portal
 {
  private:
	Point		d;							// Dimensions of the window (for text version in text columns & rows)
	WINDOW *	window;						// For this text version - this is an ncurses window
	char *		msg;						// pending message to display and delete at next opportunity
	bool		msgDirty;					// Have we displayed a message and it's dirty?

  public:
	Portal();								// Create the window (or clear the screen)
	~Portal();								// Close the window (or say good bye)

	int			H() { return d.y; }			// Height
	int			W() { return d.x; }			// Width

	void		Clear();					// Clear it
	int			GetKey();					// Key press
	void		Update();					// Put the offscreen work on screen
	void		Frame();					// Frame the window or a box
	void		Frame(const char * title,
					const char * cmd,
					const char * score);		// Frame the window or a box with strings
	void		Frame(Rect * r);				// Frame a box
	void		Sprite(int, int, SpriteNum);	// Put a sprite at a location
	void		Text(const char * s,
					int line,
					Justification j);			// Justification
	void		Dbg(const char * s);			// Show a debug message (won't go away)
	void		Msg(const char * s);			// Show a message for 3 seconds at bottom center
	char *		Msg()	{ return msg; }			// Get the mssage buffer to write into for next opportunity
	bool		MsgDirty() { return msgDirty; }	// Is message area in need of cleaning?
 };


class Snake
 {
  private:
	Points *	head;						// List of snake points
	Rect		cage;						// Should probably be the same as the Game's "box" - what the snake can move around in
	time_t		delay;						// Milliseconds between move ... 1000 is a second
	int			dY;							// 1 or -1 in Y direction
	int			dX;							// 1 or -1 in X direction
	int			adder;						// When an apple is eaten, add here - this will pin the tail for this many next moves

	Portal *	portal;						// What we are drawing into
	time_t		last;						// Last move... move again when the delay + this is < current time

	void DrawHead();						// Draw snake's head
	void DrawNeck();						// Draw a snake segment
	void DrawBlank(Point p);				// Blank (end of tail for example)

  public:
  	Snake()		{ head = NULL;}					// Create a snake ... with no head (yet)
	~Snake();

	int			Length();						// Length of snake
	bool		Eaten(Point p);					// Are we munching this point?
	bool		Suicide();						// Hit ourselves?
	bool		Collide(Point p, bool skip);	// Does p hit the snake?
	void		Start(Portal *p,
					Rect * box);				// Initialize the snake... place it somewhere and associate the portal so it can draw itself
	bool		Move(time_t t,
					const Rect * bounds);		// Returns true if the move caused death
	void		Control(int x, int y);			// Request direction (-1 or 1 in each parameter)

	Point		Loc()	{ return head->P(); }
	int			X() 	{ return head->X(); }	// Location of snake
	int			Y()		{ return head->Y(); }
 };


class Game
 {
  private:
	Point		apple;					// Our things for the game
	Snake		snake;

	Portal		portal;					// Where we draw
	Timer		timer;					// Keep track of time etc.
	Rect		box;					// Our game dimensions (1,3 to W-2, H-4)

	int			score;					// How many points
	int			scored;					// How many points shown on screen
	long		ticker;					// Just a counter as we do tasks - spin the Twirly
	int			anims;					// Count up to AnimationSpeed and reset
	time_t		msgTime;				// Last time message was posted - remove after some seconds

	bool		dead;					// Snake dead
	bool		quit;

	void	Clear();					// Clear portal and add frame
	void	Help();						// Instructions front & center
	void	AddApple();					// Place them on the board
	void	AddSnake();

  public:
	Game();								// Constructor
	~Game();							// Destructor

	void	Start();					// Start things going
	void	Title();					// Draw our title screen
	void	Task();						// Do whatever is next
	void	Idle();						// Update our idle time and don't eat all the CPU
	void	Finish();					// Game over notice


	bool	Quit() { return quit; };	// Time to quit?
	bool	Dead() { return dead; };	// Time to stand still?
 };



Points ** Points::Tail()	// Return a pointer to the last item in the list of points (to remove it for example)
 {
	// Find the tail and remove it
	for (Points ** s = &next; *s; )
		if (!(*s)->next)	// Nothing after me - must be tail
			return s;
		else
			s = &(*s)->next->next;

	return 0;
 }


int Points::Length()		// Return length of list of points
 {
	int		n = 1;
	for (Points * p = next; p; p = p->next)
		++ n;
	return n;
 }


Portal::Portal()
 {
	// Create ncurses (see man ncurses) window ... port this to whatever widgets you want
	window = initscr();

	if (!window)
	 {
		puts("Curses error!");
		exit(33);
	 }

	// Space for a message
	msg = new char[MessageSize];
	msgDirty = false;

	// To allow keypresses w/o seeing them
	cbreak();
	noecho();
	nonl();
	intrflush(stdscr, FALSE);
	keypad(stdscr, TRUE);
	nodelay(window, TRUE);		// Don't stop the program waiting for a key - just gimme an error

	clear();					// Clear screen (buffer)
	curs_set(0);				// Hide cursor

	// Screen Dimensions
	getmaxyx(window, d.y, d.x);

	// Clear screen...
	Update();
 }


Portal::~Portal()
 {
	if (window)
	 {
		window = NULL;
		endwin();
		// cout << "W: " << W() << ", H: " << H() << endl;
	 }

	if (msg)
	 {
		delete msg;
		msg = 0;
	 }
 }


int Portal::GetKey()
 {
	return getch();
 }


void Portal::Update()
 {
	refresh();
 }


void Portal::Clear()
 {
	// clear ncurss screen
	clear();
 }


void Portal::Sprite(int x, int y, SpriteNum n)
 {
	// Draw on ncurses
	mvaddnstr(y, x, Sprites + n, 1);
 }


void Portal::Frame(const char * title, const char * cmd, const char * score)
 {
	box(window, 0, 0);
	mvhline(2, 2, 0, W() - 4);
	mvhline(H() - 3, 2, 0, W() - 4);
	Text(title, 1, Center);
	Text(cmd, -2, Left);
	Text(score, -2, Right);
 }


void Portal::Text(const char * s, int line, Justification j)
 {
	int	x;

	// Negative line number is from bottom
	if (line < 0)
		line += H();

	// Justify left or right (2 char border) or center it
	switch(j)
	 {
	  case Left:	x = 2;	break;
	  case Right:	x = W() - strlen(s) - 2; break;
	  default:
	  	x = (W() - strlen(s)) / 2;
	  break;
	 }

	mvaddstr(line, x, s);
 }


//
// Print a message somehwere the user can see it
//
void Portal::Msg(const char * m)
 {
	int	n = strlen(m);
	int w = W();

	// Leave space for score and isntructions
	const int saveSpc = 60;

	if (w > saveSpc) if (n < (w - saveSpc))
	 {
		// Clear area
		char * clrs = new char[w - saveSpc];
		memset(clrs, ' ', w - saveSpc);
		clrs[w - saveSpc] = 0;
		Text(clrs, -2, Center);
		delete clrs;
	 }

	// Write text
	Text(m, -2, Center);

	// Mark as dirty area if we wrote something
	msgDirty = (*m != 0);
 }


void Portal::Dbg(const char * msg)
 {
	Text(msg, 1, Left);
 }


Snake::~Snake()
 {
	if (head)
		delete head;
 }


int Snake::Length()
 {
	return head ? head->Length() : 0;
 }


void Snake::DrawHead()
 {
	if (Points * p = head)
	 {
		SpriteNum	n = SnakeHeadSpriteN;

		if (dX > 0) n = SnakeHeadSpriteE;
		if (dX < 0) n = SnakeHeadSpriteW;
		if (dY > 0) n = SnakeHeadSpriteS;

		portal->Sprite(p->X(), p->Y(), n);
	 }
 }


void Snake::DrawNeck()
 {
	if (Points * p = head)
		if (Points * n = p->Next())
			portal->Sprite(n->X(), n->Y(), SnakeNeckSprite);
 }


void Snake::DrawBlank(Point p)
 {
	portal->Sprite(p.x, p.y, BlankSprite);
 }


bool Snake::Move(time_t t, const Rect * bounds)
 {
	bool dead = false;

	// Has enough time passed to move?
	if (head) if (delay + last < t)
	 {
		last = t;

		// Where are we going?
		Point	p = head->P();

		// Move it one slot
		p.x += dX;
		p.y += dY;

		// Constrain it by the box
		if (p.x < bounds->L() || p.x >= bounds->R() || p.y < bounds->T() || p.y >= bounds->B())
			dead = true;

		// Paste in the new head
		head = new Points(head, &p);

		DrawHead();		// Update head
		DrawNeck();		// Update neck (overwrite old head)

		// Kill the tail? 
		if (adder > 0)
			--adder;
		else if (head)
		 {
			if (Points ** tail = head->Tail())
			 {
				Points * t = *tail;
				*tail = 0;
				Snake::DrawBlank(t->P());
				delete t;
			 }
		 }
	 }

	return dead;
 }


//
//	Set the snake direction to one of N,S,E,W
//
void Snake::Control(int x, int y)
 {
	if (!portal)									// Not alive yet?
		return;
	else if (x == dX && y == dY)					// Can't go same direction... although... we could theoretically accelerate by reducing the 'delay' if the user wants to go fast
	 {
		// portal->Msg("Can't go further that way.");
		portal->Msg("Power boost!");
		delay -= (delay / 20);						// 20% speedup for free
	 }
	else if (x == dX || y == dY)					// Can't go backwards ... although we could slow down by increasing 'delay'
	 {
		// portal->Msg("Can't walk over yourself");
		portal->Msg("Stepped in doggy doo!");
		delay += (delay / 20);						// 20% slowdown for free
	 }
	else
	 {
		dX = x;
		dY = y;
		last = 0;									// Go ahead and move!
	 }
 }


void Snake::Start(Portal * port, Rect * box)
 {
	portal = port;
	cage = *box;

	// Half width & height
	int	w2 = cage.W() / 2;
	int h2 = cage.H() / 2;

	// Random place for Snake (but in the quadrant away from where he's going so we don't crash right away)
	int x = random() % cage.W();
	int	y = random() % cage.H();

	// Random direction for snake to move
	switch (random() % 4)
	 {
	  case 0:	dX = -1; dY = 0; x /= 2; x += w2; break;	// Going left - so have to be on right side
	  case 1:	dX = 0; dY = 1; y /= 2; break; 				// Going down - have to be on top half
	  case 2:	dX = 1; dY = 0; x /= 2; break;				// Going right - have to be on left
	  case 3:	dX = 0; dY = -1; y /= 2; y += h2; break;	// Going up - have to be on bottom half
	 }

	// Start with a half second between moves?
	delay = 222;		// Initial delay between snake moves in milliseconds
	adder = 4;			// Initial length of snake

	head = new Points(x + cage.L(), y + cage.T());
 }


bool Snake::Eaten(Point apple)
 {
	if (head)
	 {
		// Check for Apple and head collision
		if (X() == apple.x && Y() == apple.y)
		 {
			// Increase length by 50% and speed up snake by 20%
			adder += Length() / 2;
			delay -= (delay / 20);
			return true;
		 }
	 }
	return false;
 }


bool Snake::Collide(Point loc, bool skipHead = false)		// Is this point on the snake?
 {
	// Check if we hit the point at any of our segments... walk the list
	if (!head && skipHead)	// Can't skip ahead of nothing
		return false;
	
	for (Points * p = skipHead ? head->Next() : head; p; p = p->Next())
		if (loc.x == p->X() && loc.y == p->Y())
			return true;

	return false;
 }


bool Snake::Suicide()		// Did we crash into our own neck?
 {
	return Collide(Loc(), true);
 }


Game::Game()
 {
	// Start dead
	dead = true;
	quit = false;

	// Twirly start
	ticker = 0;
	anims = 0;
	msgTime = 0;

	// Shrink area away from frame
	box.tl.x = 2;
	box.tl.y = 4;
	box.br.x = portal.W() - 2;
	box.br.y = portal.H() - 4;

	// Seed random numbers with ticker
	Idle();
	srandom(timer.MS());
 }


Game::~Game()
 {
 }


void Game::Idle()
 {
	if (++anims > AnimationSpeed)	// Don't animate too fast - this does every 16 x .03 or about half a second
	 {
		char * t;

		anims = 0;

		// Little animated doodads at the upper left and right to show we are alive
		mvaddnstr(1, 1, t = Twirly + (++ticker % strlen(Twirly)), 1);
		mvaddnstr(1, portal.W() - 2, t, 1);

		// Animate the Apple?
		if (!Dead())
			mvaddnstr(apple.y, apple.x, Applets + (ticker % strlen(Applets)), 1);
	 }

	// Share
	usleep(TickleSpeed);		// Give up some CPU ... .03 seconds

	timer.Update();

	// Show a message?
	if (*portal.Msg())
	 {
		// portal.Dbg("Post");
		portal.Msg(portal.Msg());		// Weird construct, I know... message display of the pending message
		*portal.Msg() = 0;
	 }
	else if (portal.MsgDirty())
	 {
		// portal.Dbg("Dirty...");

		if (!msgTime)		// Something written but not stamped - stamp it
		 {
			msgTime = timer.MS();;
			// portal.Dbg("Stamp");
		 }
		else if ((timer.MS() - msgTime) > 3456)	// Something written and time is up ... clear it after 3.456 seconds
		 {
			// portal.Dbg("Clearing");
			portal.Msg("");
			msgTime = 0;
		 }
	 }
 }


void Game::AddApple()
 {
	// Random place for Apple - but - Avoid snake or he'll erase it
	for (bool collision = true; collision; )
	 {
		apple.x = random() % box.W() + box.L();
		apple.y = random() % box.H() + box.T();

		collision = snake.Collide(apple);
		
		if (collision)
			portal.Msg("Apple collided...");
	 }

	portal.Sprite(apple.x, apple.y, AppleSprite);
 }


void Game::AddSnake()
 {
	snake.Start(&portal, &box);
 }


void Game::Start()
 {
	Clear();
	AddApple();
	AddSnake();

	// Off you go
	score = scored = 0;
	dead = false;
 }


void Game::Help()
 {
	// Instructions

	int		line = portal.H() / 2 - 2;

	portal.Text("\\=-------------------------------------------=/", line++, Center);
	portal.Text("   Press N to begin a new game at any time   ", line++, Center);
	portal.Text("   ***   ", line++, Center);
	portal.Text("   Use arrow keys or A,W,S,D to move   ", line++, Center);
	portal.Text("/=-------------------------------------------=\\", line++, Center);

	portal.Update();
 }


void Game::Title()
 {
	// Start screen with frame
	Clear();
	Help();
 }


void Game::Clear()
 {
 	// Automatic border
 	portal.Clear();
 	portal.Frame((char *)"Welcome to Keanu Snake", (char *)"Commands: N, asdw, Q, <v^>", (char *)"Score: 0000");
 }


void Game::Task()
 {
	// If snake is not dead... move it and update screen
	if (!Dead())
	 {
		dead |= snake.Move(timer.MS(), &box);
		portal.Update();

		// Collision?
		if (snake.Eaten(apple))
		 {
			// Score it and add a new Apple
			scored += snake.Length();
			AddApple();
		 }

		// Suicide?
		if ((dead |= snake.Suicide()))
		 {
			portal.Msg("Whah whah whah whah.... sorry, you biffed");
			Help();
		 }

		// Update score?
		if (scored != score)
		 {
			char	scores[32];
			sprintf(scores, "Score: %04d", score = scored);
			portal.Text(scores, -2, Right);
			portal.Msg("Congratulations!");
		 }
	 }

	char	c;

	// Move Snake or start game or quit
	switch (c = portal.GetKey())
	 {
	  // Move?
	  case 'A': case 'a': case 4:	snake.Control(-1, 0);	break;
	  case 'S': case 's': case 2:	snake.Control(0,  1);	break;
	  case 'D': case 'd': case 5:	snake.Control(1,  0);	break;
	  case 'W': case 'w': case 3:	snake.Control(0, -1);	break;

	  // New game
	  case 'N': case 'n':	Start();	break;

	  // Quit?
	  case 'Q': case 'q': quit = true;	break;

	  // No key ready?
	  case -1:	  break;

	  default:
		sprintf(portal.Msg(), "Unknown Key: %d", (int)c);
	  break;
	 }

	Idle();
 }


int main(int argc, char * argv[], char * env[])
 {
	if (Game * myGame = new Game)
	 {
		myGame->Title();

		while (!myGame->Quit())
			myGame->Task();

		delete myGame;
	 }

	puts("\n\n\tThank you for spending some quality time with the Snake!\n\n\t(c)2014 Akua, Inc. Version 1.0\n");

	return 0;
 }
